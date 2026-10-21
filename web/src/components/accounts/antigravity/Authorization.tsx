import { Modal } from '../../Modal'
import { Spinner } from '../../Spinner'
import type { useAntigravityLogin } from '../../../hooks/antigravity/useLogin'

interface Props {
  google: ReturnType<typeof useAntigravityLogin>
  displayName: string
  onClose: () => void
}

export function AntigravityAuthorization({ google, displayName, onClose }: Props) {
  return <Modal title="Authorize Google Antigravity" description="Connect an eligible Google account using OAuth." onClose={onClose}>
    <form className="form-stack" onSubmit={(event) => { event.preventDefault(); void google.complete(displayName) }}>
      <div className="field">
        <p>Open Google authorization, sign in, and approve access. Google will redirect to the configured callback address. The page may not load; copy its complete URL from the address bar.</p>
        <a className="button button--secondary" href={google.login?.authorization_url} target="_blank" rel="noopener noreferrer">Continue to Google</a>
      </div>
      <div className="field">
        <label htmlFor="google-callback">Callback URL <span aria-hidden="true">*</span></label>
        <input id="google-callback" type="password" value={google.callbackURL} onChange={(event) => google.setCallbackURL(event.target.value)} autoComplete="off" autoCapitalize="none" spellCheck={false} required aria-describedby="google-callback-help" placeholder="http://localhost:8085/callback?state=…&code=…" />
        <small id="google-callback-help">The URL contains a one-time code. It is used only to complete authorization and is not saved with the account.</small>
      </div>
      {google.error ? <div className="inline-alert" role="alert">{google.error}</div> : null}
      <div className="form-actions">
        <button className="button button--secondary" type="button" onClick={onClose}>Cancel</button>
        <button className="button button--secondary" type="button" disabled={google.busy} onClick={() => void google.start()}>Start again</button>
        <button className="button button--primary" type="submit" disabled={google.busy}>{google.busy ? <><Spinner /> Connecting…</> : 'Connect account'}</button>
      </div>
    </form>
  </Modal>
}
