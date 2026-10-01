import { useRef, useState } from 'react';
import { type Info, uploadDeploy } from './api';
import { DropZone } from './ds';
import { message } from './hooks';
import { collectDrop, fromFiles, type Upload } from './pack';

const megabytes = (bytes: number) => `${Math.round(bytes / (1 << 20))}MB`;

// UploadZone is the drop zone wired to Pail: it packs what's dropped, sends
// it as a deploy and shows how far along it is.
export function UploadZone(props: {
  info: Info | null;
  hint?: string;
  // pailFor names the pail an upload goes to. It may throw to refuse one.
  pailFor: (upload: Upload) => string;
  // onBusy says when an upload starts and stops.
  onBusy?: (busy: boolean) => void;
  // onDone runs once the server has the upload and its deploy has begun.
  onDone: (pail: string) => void;
}) {
  const [progress, setProgress] = useState<number | undefined>();
  const [fileName, setFileName] = useState<string | undefined>();
  const [error, setError] = useState('');
  // What the drop event itself collected, for the DropZone's callback to use.
  const dropped = useRef<Promise<Upload> | null>(null);

  const send = async (pending: Promise<Upload>) => {
    setError('');
    setFileName('Getting it ready');
    setProgress(0);
    props.onBusy?.(true);
    try {
      const upload = await pending;
      const pail = props.pailFor(upload);
      const limit = props.info?.limits.max_upload_size;
      if (limit && upload.archive.size > limit) {
        throw new Error(
          `That’s ${megabytes(upload.archive.size)}, over the ${megabytes(limit)} this Pail takes. Raise PAIL_MAX_UPLOAD_SIZE on the server to send more.`,
        );
      }
      setFileName(upload.fileName);
      await uploadDeploy(pail, upload.archive, upload.fileName, setProgress);
      props.onBusy?.(false);
      props.onDone(pail);
    } catch (err) {
      setProgress(undefined);
      setFileName(undefined);
      setError(message(err));
      props.onBusy?.(false);
    }
  };

  return (
    <>
      {/* The DropZone hands on a file list, which is empty for a dropped
          folder. The folder's contents are collected here, during the same
          drop event, before the DropZone's own handler runs. */}
      <div
        onDropCapture={(e) => {
          dropped.current = collectDrop(e.dataTransfer.items);
        }}
      >
        <DropZone
          hint={props.hint}
          progress={progress}
          fileName={fileName}
          onFiles={(files) => {
            const pending = dropped.current ?? (files.length ? fromFiles(files) : null);
            dropped.current = null;
            if (pending && progress === undefined) send(pending);
          }}
        />
      </div>
      {error && (
        <div className="pl-note pl-note-failed" role="alert">
          {error}
        </div>
      )}
    </>
  );
}
