import { useState, type FormEvent } from 'react'
import { errorMessage, request } from '../api'
import { GestaCredit } from '../components/GestaCredit'
import { Spinner } from '../components/Spinner'

interface LoginPageProps {
  onSuccess: () => void
  connectionError?: string
  onRetryConnection?: () => void
}

function LoginIntro() {
  return <section className="login-intro" aria-labelledby="login-intro-title">
    <div className="brand brand--large"><img className="brand__logo" src="/brand/subpool-wordmark-inverse.svg" alt="Subpool" /></div>
    <div className="login-intro__copy">
      <p className="eyebrow">Enterprise subscription control</p>
      <h1 id="login-intro-title">Allocate capacity.<br />Keep teams moving.</h1>
      <p>Pool authorized AI subscriptions, distribute employee access, and govern quota usage without storing conversations.</p>
    </div>
  </section>
}

interface LoginFormProps extends Pick<LoginPageProps, 'connectionError' | 'onRetryConnection'> {
  password: string
  passwordError: string
  submitError: string
  submitting: boolean
  username: string
  usernameError: string
  onPasswordBlur: (value: string) => void
  onPasswordChange: (value: string) => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  onUsernameBlur: (value: string) => void
  onUsernameChange: (value: string) => void
}

function LoginForm(props: LoginFormProps) {
  return <section className="login-panel" aria-labelledby="login-title">
    <div className="login-form-wrap">
      <p className="eyebrow">Administrator access</p>
      <h2 id="login-title">Sign in to Subpool</h2>
      <p className="login-help">Use the credentials configured through <code>SUBPOOL_ADMIN_*</code>.</p>
      {props.connectionError ? <div className="inline-alert" role="alert">
        <strong>Cannot reach Subpool</strong><span>{props.connectionError}</span>
        {props.onRetryConnection ? <button type="button" onClick={props.onRetryConnection}>Retry connection</button> : null}
      </div> : null}
      <form className="form-stack" onSubmit={props.onSubmit} noValidate>
        <div className="field">
          <label htmlFor="username">Username <span aria-hidden="true">*</span></label>
          <input id="username" name="username" autoComplete="username" value={props.username} onChange={(event) => props.onUsernameChange(event.target.value)} onBlur={(event) => props.onUsernameBlur(event.target.value)} aria-invalid={Boolean(props.usernameError)} aria-describedby={props.usernameError ? 'username-error' : undefined} />
          {props.usernameError ? <span className="field__error" id="username-error">{props.usernameError}</span> : null}
        </div>
        <div className="field">
          <label htmlFor="password">Password <span aria-hidden="true">*</span></label>
          <input id="password" name="password" type="password" autoComplete="current-password" value={props.password} onChange={(event) => props.onPasswordChange(event.target.value)} onBlur={(event) => props.onPasswordBlur(event.target.value)} aria-invalid={Boolean(props.passwordError)} aria-describedby={props.passwordError ? 'password-error' : undefined} />
          {props.passwordError ? <span className="field__error" id="password-error">{props.passwordError}</span> : null}
        </div>
        {props.submitError ? <div className="inline-alert" role="alert">{props.submitError}</div> : null}
        <button className="button button--primary button--wide" type="submit" disabled={props.submitting}>{props.submitting ? <><Spinner /> Signing in…</> : 'Enter console'}</button>
      </form>
      <p className="login-security">Credentials stay in the server environment and are never stored in the database.</p>
    </div>
    <GestaCredit className="login-panel__credit" />
  </section>
}

export function LoginPage({ onSuccess, connectionError, onRetryConnection }: LoginPageProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [usernameError, setUsernameError] = useState('')
  const [passwordError, setPasswordError] = useState('')
  const [submitError, setSubmitError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  function validateUsername(value: string) {
    const message = value.trim() ? '' : 'Enter the administrator username.'
    setUsernameError(message)
    return !message
  }

  function validatePassword(value: string) {
    const message = value ? '' : 'Enter the administrator password.'
    setPasswordError(message)
    return !message
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSubmitError('')
    if (!validateUsername(username) || !validatePassword(password)) return

    setSubmitting(true)
    try {
      await request('/api/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username: username.trim(), password }),
      })
      setPassword('')
      onSuccess()
    } catch (error) {
      setSubmitError(errorMessage(error))
    } finally {
      setSubmitting(false)
    }
  }

  return <main className="login-page">
    <LoginIntro />
    <LoginForm
      connectionError={connectionError} onRetryConnection={onRetryConnection}
      username={username} password={password} usernameError={usernameError} passwordError={passwordError}
      submitError={submitError} submitting={submitting} onSubmit={submit}
      onUsernameChange={setUsername} onPasswordChange={setPassword}
      onUsernameBlur={validateUsername} onPasswordBlur={validatePassword}
    />
  </main>
}
