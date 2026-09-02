import { Modal } from '../Modal'
import { SelectMenu } from '../SelectMenu'
import { Spinner } from '../Spinner'
import type { ConnectProvider } from '../../hooks/useConnectAccount'

interface ConnectAccountDialogProps {
  apiKey: string
  baseURL: string
  busy: boolean
  displayName: string
  error: string
  fieldErrors: Record<string, string>
  provider: ConnectProvider
  onAPIKeyChange: (value: string) => void
  onBaseURLChange: (value: string) => void
  onClose: () => void
  onDisplayNameChange: (value: string) => void
  onFieldErrorsChange: (value: Record<string, string>) => void
  onProviderChange: (value: ConnectProvider) => void
  onSubmit: () => void
}

export function ConnectAccountDialog(props: ConnectAccountDialogProps) {
  return <Modal title="Connect provider account" description={props.provider === 'codex' ? 'Subpool will open the Codex authorization flow.' : 'Add an OpenAI-compatible Base URL and its upstream API key.'} onClose={props.onClose}>
    <form className="form-stack" onSubmit={(event) => { event.preventDefault(); props.onSubmit() }} noValidate>
      <div className="field"><label htmlFor="account-provider">Provider <span aria-hidden="true">*</span></label><SelectMenu id="account-provider" value={props.provider} options={[{ value: 'codex', label: 'Codex subscription' }, { value: 'openai_compatible', label: 'OpenAI-compatible API' }]} onChange={(value) => props.onProviderChange(value as ConnectProvider)} /></div>
      <div className="field"><label htmlFor="account-name">Display name <span aria-hidden="true">*</span></label><input id="account-name" value={props.displayName} onChange={(event) => props.onDisplayNameChange(event.target.value)} onBlur={() => props.onFieldErrorsChange({ ...props.fieldErrors, displayName: props.displayName.trim() ? '' : 'Enter a display name.' })} aria-invalid={Boolean(props.fieldErrors.displayName)} aria-describedby={props.fieldErrors.displayName ? 'account-name-error' : undefined} placeholder={props.provider === 'codex' ? 'Primary Codex account' : 'Production endpoint'} />{props.fieldErrors.displayName ? <small id="account-name-error" className="field__error">{props.fieldErrors.displayName}</small> : null}</div>
      {props.provider === 'openai_compatible' ? <>
        <div className="field"><label htmlFor="account-base-url">Base URL <span aria-hidden="true">*</span></label><input id="account-base-url" type="url" value={props.baseURL} onChange={(event) => props.onBaseURLChange(event.target.value)} aria-invalid={Boolean(props.fieldErrors.baseURL)} aria-describedby={props.fieldErrors.baseURL ? 'account-base-url-error' : 'account-base-url-help'} placeholder="https://api.example.com/v1" autoCapitalize="none" autoCorrect="off" /><small id="account-base-url-help">Include the API version path. Subpool appends /chat/completions or /responses.</small>{props.fieldErrors.baseURL ? <small id="account-base-url-error" className="field__error">{props.fieldErrors.baseURL}</small> : null}</div>
        <div className="field"><label htmlFor="account-api-key">Upstream API key <span aria-hidden="true">*</span></label><input id="account-api-key" type="password" value={props.apiKey} onChange={(event) => props.onAPIKeyChange(event.target.value)} onBlur={() => props.onFieldErrorsChange({ ...props.fieldErrors, apiKey: props.apiKey.trim() ? '' : 'Enter the upstream API key.' })} aria-invalid={Boolean(props.fieldErrors.apiKey)} aria-describedby={props.fieldErrors.apiKey ? 'account-api-key-error' : 'account-api-key-help'} placeholder="sk-…" autoComplete="off" /><small id="account-api-key-help">Encrypted before storage and never returned by the API.</small>{props.fieldErrors.apiKey ? <small id="account-api-key-error" className="field__error">{props.fieldErrors.apiKey}</small> : null}</div>
      </> : null}
      {props.error ? <div className="inline-alert" role="alert">{props.error}</div> : null}
      <div className="form-actions"><button className="button button--secondary" type="button" onClick={props.onClose}>Cancel</button><button className="button button--primary" type="submit" disabled={props.busy}>{props.busy ? <><Spinner /> Connecting…</> : props.provider === 'codex' ? 'Continue to Codex' : 'Connect endpoint'}</button></div>
    </form>
  </Modal>
}
