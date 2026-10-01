# Pail runs this for each request to a Ruby function, with the function's own
# file as its argument. A file that defines handler(req, res) is a handler:
# it is called with the request and a response to fill in. Any other file is
# a program that has answered for itself, by writing CGI.

require "json"
require "uri"

module Pail
  # The request, from CGI's variables.
  class Request
    attr_reader :method, :path, :url, :query, :headers, :body

    def initialize(env, body)
      @method = (env["REQUEST_METHOD"] || "GET").upcase
      @path = env["PATH_INFO"].to_s.empty? ? "/" : env["PATH_INFO"]
      search = env["QUERY_STRING"].to_s
      @url = env["REQUEST_URI"] || (search.empty? ? @path : "#{@path}?#{search}")
      @query = URI.decode_www_form(search).to_h
      @headers = {}
      env.each { |key, value| @headers[key[5..].downcase.tr("_", "-")] = value if key.start_with?("HTTP_") }
      @headers["content-type"] = env["CONTENT_TYPE"] unless env["CONTENT_TYPE"].to_s.empty?
      @headers["content-length"] = body.bytesize.to_s unless body.empty?
      @body = body
    end

    def text
      @body.dup.force_encoding(Encoding::UTF_8)
    end

    def json
      JSON.parse(text)
    end

    def form
      URI.decode_www_form(text).to_h
    end
  end

  # The response a handler fills in.
  class Response
    def initialize
      @status = 200
      @headers = []
      @body = "".b
      @sent = false
    end

    def sent?
      @sent
    end

    def status(code)
      unless code.is_a?(Integer) && code.between?(200, 599)
        raise ArgumentError, "res.status takes a status from 200 to 599, like 404. Got #{code.inspect}."
      end
      @status = code
      self
    end

    # Sets a header, in place of any by that name.
    def set(name, value)
      @headers.reject! { |have, _| have.casecmp?(name.to_s) }
      append(name, value)
    end

    # Adds a header, beside any by that name: one Set-Cookie after another.
    def append(name, value)
      name, value = name.to_s, value.to_s
      raise ArgumentError, "#{name.inspect} isn't a header's name." unless name.match?(/\A[\w!#$%&'*+.^`|~-]+\z/)
      raise ArgumentError, "The #{name} header can't hold a line break." if value.match?(/[\r\n]/)
      @headers << [name, value]
      self
    end

    def content_type(type)
      set("Content-Type", type)
    end

    # Sends text or bytes as they are, and anything else as JSON.
    def send(body = nil)
      raise "The response was already sent." if @sent
      type = "application/json"
      case body
      when nil
        type = nil
      when String
        type = body.encoding == Encoding::BINARY ? "application/octet-stream" : "text/plain; charset=utf-8"
        @body = body.b
      else
        @body = JSON.generate(body).b
      end
      content_type(type) if type && !typed?
      @sent = true
      self
    end

    def json(value)
      content_type("application/json") unless typed?
      send(JSON.generate(value))
    end

    def redirect(location, status = 302)
      status(status).set("Location", location).send
    end

    def to_cgi
      lines = ["Status: #{@status}"] + @headers.map { |name, value| "#{name}: #{value}" }
      (lines.join("\r\n") + "\r\n\r\n").b + @body
    end

    private

    def typed?
      @headers.any? { |name, _| name.casecmp?("content-type") }
    end
  end

  def self.run(path)
    # Which kind the file is isn't known until it has run, so what it writes
    # to standard output meanwhile is held in a file: the response if it is a
    # program, and something for the pail's output if it is a handler.
    out = STDOUT.dup
    out.binmode
    out.sync = true
    name = File.join(ENV["TMPDIR"] || "/tmp", ".pail-held-#{Process.pid}")
    held = File.open(name, File::RDWR | File::CREAT | File::EXCL, 0o600)
    File.unlink(name)
    STDOUT.reopen(held)

    # The file runs as the program it would be were it run by itself.
    $PROGRAM_NAME = path
    ARGV.clear
    found = false
    begin
      load path
      found = TOPLEVEL_BINDING.receiver.respond_to?(:handler, true)
    ensure
      # Standard output is the response, so what a handler prints goes to
      # standard error instead: the pail's output.
      to = found ? STDERR : out
      STDOUT.flush
      STDOUT.reopen(to)
      STDOUT.sync = true
      held.rewind
      IO.copy_stream(held, to)
      to.flush
      held.close
    end
    return unless found

    length = ENV["CONTENT_LENGTH"].to_i
    res = Response.new
    result = TOPLEVEL_BINDING.receiver.method(:handler).call(Request.new(ENV, length > 0 ? STDIN.binmode.read(length).to_s : "".b), res)
    res.send(result) if !res.sent? && (result.is_a?(String) || result.is_a?(Hash) || result.is_a?(Array))
    out.write(res.to_cgi)
  end
end

Pail.run(File.expand_path(ARGV[0]))
