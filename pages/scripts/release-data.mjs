const repository = 'gesta-run/subpool'

export async function fetchAllGithubReleases(requestJSON, repositoryName = repository) {
  if (typeof requestJSON !== 'function') throw new TypeError('A GitHub request function is required')

  const releases = []
  for (let page = 1; ; page += 1) {
    const batch = await requestJSON(`/repos/${repositoryName}/releases?per_page=100&page=${page}`)
    if (!Array.isArray(batch)) throw new TypeError('GitHub releases response must be an array')
    releases.push(...batch)
    if (batch.length < 100) return releases
  }
}

export function createReleaseManifest(releases, commits = {}, generatedAt = new Date().toISOString()) {
  if (!Array.isArray(releases)) throw new TypeError('GitHub releases must be an array')

  const normalized = releases
    .filter((release) => release && !release.draft && !release.prerelease && release.tag_name)
    .map((release) => {
      const commitSHA = commits[release.tag_name]
      if (!commitSHA) throw new Error(`Missing commit SHA for ${release.tag_name}`)
      return {
        tag: release.tag_name,
        name: release.name || release.tag_name,
        published_at: release.published_at || release.created_at,
        commit_sha: commitSHA,
        notes_html: release.body_html || '<p>No release notes were provided.</p>',
      }
    })
    .sort((left, right) => Date.parse(right.published_at) - Date.parse(left.published_at))

  return {
    repository: {
      name: 'Subpool',
      image: 'public.ecr.aws/cloudpilotai/subpool',
    },
    generated_at: generatedAt,
    releases: normalized,
  }
}

export function githubHeaders(token = '') {
  const headers = {
    Accept: 'application/vnd.github.html+json',
    'X-GitHub-Api-Version': '2022-11-28',
    'User-Agent': 'subpool-release-pages',
  }
  if (token) headers.Authorization = `Bearer ${token}`
  return headers
}
