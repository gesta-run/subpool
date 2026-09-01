import { useState, type FormEvent } from 'react'
import { errorMessage, request } from '../api'

interface LoginPageProps {
  onSuccess: () => void
  connectionError?: string
  onRetryConnection?: () => void
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

  return (
    <main className="login-page">
      <section className="login-intro" aria-labelledby="login-intro-title">
        <div className="brand brand--large">
          <span className="brand__mark" aria-hidden="true"><i /><i /><i /></span>
          <div><strong>SUBPOOL</strong><span>CONTROL PLANE</span></div>
        </div>
        <div className="login-intro__copy">
          <p className="eyebrow">Codex subscription routing</p>
          <h1 id="login-intro-title">One gateway.<br />A calmer pool.</h1>
          <p>Configure upstream accounts, issue employee keys, and observe token use without storing conversations.</p>
        </div>
        <div className="login-intro__diagram" aria-hidden="true">
          <span>API KEY</span><i /><span>POOL</span><i /><span>CODEX</span>
        </div>
        <p className="login-intro__foot">SELF-HOSTED / DOCKER COMPOSE / PHASE 01</p>
      </section>
      <section className="login-panel" aria-labelledby="login-title">
        <div className="login-form-wrap">
          <p className="eyebrow">Administrator access</p>
          <h2 id="login-title">Sign in to your instance</h2>
          <p className="login-help">Use the credentials configured through <code>SUBPOOL_ADMIN_*</code>.</p>
          {connectionError ? (
            <div className="inline-alert" role="alert">
              <strong>Cannot reach Subpool</strong>
              <span>{connectionError}</span>
              {onRetryConnection ? <button type="button" onClick={onRetryConnection}>Retry connection</button> : null}
            </div>
          ) : null}
          <form className="form-stack" onSubmit={submit} noValidate>
            <div className="field">
              <label htmlFor="username">Username <span aria-hidden="true">*</span></label>
              <input
                id="username"
                name="username"
                autoComplete="username"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                onBlur={(event) => validateUsername(event.target.value)}
                aria-invalid={Boolean(usernameError)}
                aria-describedby={usernameError ? 'username-error' : undefined}
              />
              {usernameError ? <span className="field__error" id="username-error">{usernameError}</span> : null}
            </div>
            <div className="field">
              <label htmlFor="password">Password <span aria-hidden="true">*</span></label>
              <input
                id="password"
                name="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                onBlur={(event) => validatePassword(event.target.value)}
                aria-invalid={Boolean(passwordError)}
                aria-describedby={passwordError ? 'password-error' : undefined}
              />
              {passwordError ? <span className="field__error" id="password-error">{passwordError}</span> : null}
            </div>
            {submitError ? <div className="inline-alert" role="alert">{submitError}</div> : null}
            <button className="button button--primary button--wide" type="submit" disabled={submitting}>
              {submitting ? <><span className="spinner spinner--dark" /> Signing in…</> : 'Enter console'}
            </button>
          </form>
          <p className="login-security">Credentials stay in the server environment and are never stored in the database.</p>
        </div>
      </section>
    </main>
  )
}
