import { type ReactNode, useEffect, useState } from 'react';
import { getInfo, hasToken, type Info, onTokenRejected } from './api';
import { TopBar } from './ds';
import { navigate, usePath } from './router';
import { NewPail } from './screens/NewPail';
import { PailList } from './screens/PailList';
import { PailPage } from './screens/PailPage';
import { TokenStep } from './screens/TokenStep';

export function App() {
  const [authed, setAuthed] = useState(hasToken);
  const [info, setInfo] = useState<Info | null>(null);
  const path = usePath();

  useEffect(() => onTokenRejected(() => setAuthed(false)), []);
  useEffect(() => {
    if (authed && !info) getInfo().then(setInfo, () => {});
  }, [authed, info]);

  // Until Pail says its base domain, the address bar is the best guess.
  const host = info?.base_domain ?? window.location.hostname;

  if (!authed) {
    return (
      <div className="pl-app">
        <TopBar host={host}>{null}</TopBar>
        <TokenStep
          onDone={(info) => {
            setInfo(info);
            setAuthed(true);
          }}
        />
      </div>
    );
  }

  let screen: ReactNode;
  const pail = path.match(/^\/pails\/([^/]+)\/?$/);
  if (pail) screen = <PailPage key={pail[1]} name={decodeURIComponent(pail[1])} info={info} />;
  else if (path === '/new') screen = <NewPail host={host} info={info} />;
  else screen = <PailList host={host} info={info} />;

  return (
    <div className="pl-app">
      <TopBar host={host} onNew={() => navigate('/new')} />
      {screen}
    </div>
  );
}
