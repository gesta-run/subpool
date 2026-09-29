import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { createReleaseManifest, fetchAllGithubReleases, githubHeaders } from './release-data.mjs'

const repository = process.env.GITHUB_REPOSITORY || 'gesta-run/subpool'
const pagesRoot = fileURLToPath(new URL('../', import.meta.url))
const repositoryRoot = fileURLToPath(new URL('../../', import.meta.url))
const sourceRoot = `${pagesRoot}site`
const outputRoot = `${pagesRoot}dist`
const offline = process.argv.includes('--offline')

async function githubJSON(path) {
  const response = await fetch(`https://api.github.com${path}`, {
    headers: githubHeaders(process.env.GITHUB_TOKEN),
  })
  if (!response.ok) throw new Error(`GitHub API ${path} returned ${response.status}`)
  return response.json()
}

async function fetchManifest() {
  const releases = await fetchAllGithubReleases(githubJSON, repository)
  const stableReleases = releases.filter((release) => !release.draft && !release.prerelease)
  const commits = {}
  for (let offset = 0; offset < stableReleases.length; offset += 10) {
    await Promise.all(stableReleases.slice(offset, offset + 10).map(async (release) => {
      const commit = await githubJSON(`/repos/${repository}/commits/${encodeURIComponent(release.tag_name)}`)
      commits[release.tag_name] = commit.sha
    }))
  }
  return createReleaseManifest(releases, commits)
}

async function build() {
  await rm(outputRoot, { recursive: true, force: true })
  await mkdir(`${outputRoot}/assets`, { recursive: true })
  await cp(sourceRoot, outputRoot, { recursive: true })
  await cp(`${repositoryRoot}web/public/brand/subpool-wordmark-inverse.svg`, `${outputRoot}/assets/subpool-wordmark-inverse.svg`)
  await cp(`${repositoryRoot}web/public/brand/subpool-favicon.svg`, `${outputRoot}/assets/subpool-favicon.svg`)

  const manifest = offline
    ? JSON.parse(await readFile(`${sourceRoot}/releases.json`, 'utf8'))
    : await fetchManifest()
  if (!Array.isArray(manifest.releases)) throw new Error('Release manifest must contain a releases array')
  await writeFile(`${outputRoot}/releases.json`, `${JSON.stringify(manifest, null, 2)}\n`)
}

await build()
