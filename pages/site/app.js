const latestRelease = document.querySelector('.latest-release')
const releaseList = document.querySelector('[data-release-list]')
const noReleaseState = document.querySelector('[data-release-none]')
const errorState = document.querySelector('[data-release-error]')

let manifest = null
let currentReleaseTag = ''

function formatDate(value, includeTime = false) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Unknown'
  const options = includeTime
    ? { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' }
    : { year: 'numeric', month: 'short', day: 'numeric' }
  return new Intl.DateTimeFormat('en', options).format(date)
}

function setText(selector, value) {
  const element = document.querySelector(selector)
  if (element) element.textContent = value
}

function safeNotesHTML(html) {
  const documentFragment = new DOMParser().parseFromString(html || '', 'text/html')
  const allowed = new Set(['BLOCKQUOTE', 'BR', 'CODE', 'EM', 'H2', 'H3', 'H4', 'HR', 'LI', 'OL', 'P', 'PRE', 'STRONG', 'UL'])
  const elements = [...documentFragment.body.querySelectorAll('*')]

  for (const element of elements) {
    if (!allowed.has(element.tagName)) {
      element.replaceWith(...element.childNodes)
      continue
    }
    for (const attribute of [...element.attributes]) element.removeAttribute(attribute.name)
  }
  return documentFragment.body.innerHTML || '<p>No release notes were provided.</p>'
}

function renderLatest(release) {
  const command = `docker pull public.ecr.aws/cloudpilotai/subpool:${release.tag}`
  setText('[data-latest-version]', release.name || release.tag)
  setText('[data-latest-label]', 'Current stable release')
  setText('[data-latest-date]', formatDate(release.published_at))
  setText('[data-latest-channel]', 'Stable')
  setText('[data-install-command]', command)
  setText('[data-artifact-tag]', `:${release.tag}`)

  const commit = document.querySelector('[data-latest-commit]')
  commit.textContent = release.commit_sha.slice(0, 12)
  const copyButton = document.querySelector('[data-copy-command]')
  copyButton.disabled = false
  copyButton.classList.remove('is-copied')
  copyButton.querySelector('span').textContent = 'Copy'
  const artifactStatus = document.querySelector('[data-artifact-status]')
  setText('[data-artifact-status-label]', 'Published')
  artifactStatus.classList.remove('is-unavailable')
  latestRelease.dataset.state = 'published'
  latestRelease.setAttribute('aria-busy', 'false')
}

function renderLatestUnavailable(title, status = 'Unavailable', state = 'unavailable') {
  setText('[data-latest-label]', 'Release status')
  setText('[data-latest-version]', title)
  setText('[data-latest-date]', '\u2014')
  setText('[data-latest-channel]', '\u2014')
  setText('[data-install-command]', 'No container image available')
  setText('[data-artifact-tag]', ':unavailable')

  const commit = document.querySelector('[data-latest-commit]')
  commit.textContent = '\u2014'
  const copyButton = document.querySelector('[data-copy-command]')
  copyButton.disabled = true
  copyButton.classList.remove('is-copied')
  copyButton.querySelector('span').textContent = 'Copy'
  const artifactStatus = document.querySelector('[data-artifact-status]')
  setText('[data-artifact-status-label]', status)
  artifactStatus.classList.add('is-unavailable')
  latestRelease.dataset.state = state
  latestRelease.setAttribute('aria-busy', 'false')
}

function renderLatestLoading() {
  renderLatestUnavailable('Loading release', 'Checking', 'loading')
  setText('[data-latest-date]', 'Checking\u2026')
  setText('[data-latest-channel]', 'Checking\u2026')
  setText('[data-install-command]', 'Checking for a published image\u2026')
  setText('[data-artifact-tag]', ':loading')
  document.querySelector('[data-artifact-status]').classList.remove('is-unavailable')
  latestRelease.setAttribute('aria-busy', 'true')
}

function releaseBadges(release) {
  const badges = []
  if (release.tag === currentReleaseTag) badges.push('<span class="release-badge release-badge--latest">Current</span>')
  return badges.join('')
}

function createReleaseEntry(release, index) {
  const article = document.createElement('article')
  article.className = 'release-entry'

  const header = document.createElement('header')
  header.className = 'release-entry__header'
  const title = document.createElement('div')
  title.className = 'release-entry__title'
  title.innerHTML = `<h3></h3>${releaseBadges(release)}<p></p>`
  title.querySelector('h3').textContent = release.name || release.tag
  title.querySelector('p').textContent = `Published ${formatDate(release.published_at)} - ${release.commit_sha.slice(0, 12)}`

  header.append(title)

  const details = document.createElement('details')
  details.className = 'release-details'
  details.open = index === 0
  const summary = document.createElement('summary')
  summary.textContent = 'Release notes'
  const notes = document.createElement('div')
  notes.className = 'release-notes'
  notes.innerHTML = safeNotesHTML(release.notes_html)
  details.append(summary, notes)
  article.append(header, details)
  return article
}

function renderReleases() {
  const releases = manifest?.releases || []
  releaseList.replaceChildren(...releases.map(createReleaseEntry))
  releaseList.hidden = releases.length === 0
  noReleaseState.hidden = true
  errorState.hidden = true
  releaseList.setAttribute('aria-busy', 'false')
}

function renderNoReleases() {
  releaseList.replaceChildren()
  releaseList.hidden = true
  noReleaseState.hidden = false
  errorState.hidden = true
  releaseList.setAttribute('aria-busy', 'false')
}

async function loadManifest() {
  renderLatestLoading()
  releaseList.hidden = false
  noReleaseState.hidden = true
  errorState.hidden = true
  releaseList.setAttribute('aria-busy', 'true')
  try {
    const response = await fetch('./releases.json', { cache: 'no-store' })
    if (!response.ok) throw new Error(`Release manifest returned ${response.status}`)
    const payload = await response.json()
    if (!Array.isArray(payload.releases)) throw new Error('Release manifest is invalid')
    manifest = payload
    setText('[data-generated-at]', `Synced ${formatDate(payload.generated_at, true)}`)
    if (payload.releases.length === 0) {
      currentReleaseTag = ''
      renderLatestUnavailable('No releases yet', 'Not published', 'empty')
      renderNoReleases()
      return
    }
    const current = payload.releases[0]
    currentReleaseTag = current.tag
    renderLatest(current)
    renderReleases()
  } catch {
    manifest = null
    renderLatestUnavailable('Release data unavailable')
    releaseList.replaceChildren()
    releaseList.hidden = true
    noReleaseState.hidden = true
    errorState.hidden = false
    releaseList.setAttribute('aria-busy', 'false')
    setText('[data-generated-at]', 'Release sync unavailable')
    setText('[data-release-error-message]', 'The release manifest could not be loaded. Check your connection and try again.')
  }
}

document.querySelector('[data-retry]').addEventListener('click', () => void loadManifest())
document.querySelector('[data-copy-command]').addEventListener('click', async (event) => {
  const button = event.currentTarget
  const command = document.querySelector('[data-install-command]').textContent
  try {
    await navigator.clipboard.writeText(command)
    button.classList.add('is-copied')
    button.querySelector('span').textContent = 'Copied'
    window.setTimeout(() => {
      button.classList.remove('is-copied')
      button.querySelector('span').textContent = 'Copy'
    }, 1800)
  } catch {
    button.querySelector('span').textContent = 'Unavailable'
  }
})

void loadManifest()
