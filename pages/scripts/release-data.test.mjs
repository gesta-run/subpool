import assert from 'node:assert/strict'
import test from 'node:test'
import { createReleaseManifest, fetchAllGithubReleases, githubHeaders } from './release-data.mjs'

test('fetchAllGithubReleases loads every page', async () => {
  const requests = []
  const firstPage = Array.from({ length: 100 }, (_, index) => ({ tag_name: `v1.0.${index}` }))
  const finalRelease = { tag_name: 'v0.9.0' }
  const releases = await fetchAllGithubReleases(async (path) => {
    requests.push(path)
    return path.endsWith('page=1') ? firstPage : [finalRelease]
  }, 'example/subpool')

  assert.equal(releases.length, 101)
  assert.equal(releases.at(-1), finalRelease)
  assert.deepEqual(requests, [
    '/repos/example/subpool/releases?per_page=100&page=1',
    '/repos/example/subpool/releases?per_page=100&page=2',
  ])
})

test('createReleaseManifest keeps stable releases newest first', () => {
  const manifest = createReleaseManifest([
    { tag_name: 'v0.2.0-beta.1', name: '', published_at: '2026-09-20T00:00:00Z', html_url: 'https://github.com/gesta-run/subpool/releases/tag/v0.2.0-beta.1', prerelease: true, body_html: '<p>Beta notes</p>' },
    { tag_name: 'v0.1.1', name: 'Subpool v0.1.1', published_at: '2026-09-10T00:00:00Z', html_url: 'https://github.com/gesta-run/subpool/releases/tag/v0.1.1', body_html: '<p>Stable notes</p>' },
    { tag_name: 'v0.1.0', name: 'Subpool v0.1.0', published_at: '2026-09-01T00:00:00Z', html_url: 'https://github.com/gesta-run/subpool/releases/tag/v0.1.0', prerelease: false, body_html: '<p>Stable notes</p>' },
    { tag_name: 'v0.3.0', draft: true, html_url: 'https://example.com/draft' },
  ], {
    'v0.1.1': '3333333333333333333333333333333333333333',
    'v0.1.0': '1111111111111111111111111111111111111111',
  }, '2026-09-29T00:00:00Z')

  assert.equal(manifest.releases.length, 2)
  assert.equal(manifest.releases[0].tag, 'v0.1.1')
  assert.equal(manifest.releases[0].name, 'Subpool v0.1.1')
  assert.equal(manifest.releases[0].prerelease, undefined)
  assert.equal(manifest.releases[0].url, undefined)
  assert.equal(manifest.releases[0].commit_url, undefined)
  assert.equal(manifest.releases[1].commit_sha, '1111111111111111111111111111111111111111')
  assert.equal(manifest.repository.url, undefined)
  assert.equal(manifest.generated_at, '2026-09-29T00:00:00Z')
})

test('createReleaseManifest provides empty release notes', () => {
  const manifest = createReleaseManifest([
    { tag_name: 'v0.1.0', created_at: '2026-09-01T00:00:00Z', html_url: 'https://github.com/gesta-run/subpool/releases/tag/v0.1.0' },
  ], { 'v0.1.0': '1111111111111111111111111111111111111111' })
  assert.equal(manifest.releases[0].notes_html, '<p>No release notes were provided.</p>')
})

test('createReleaseManifest supports repositories without published releases', () => {
  const manifest = createReleaseManifest([], {}, '2026-09-29T00:00:00Z')
  assert.deepEqual(manifest.releases, [])
})

test('createReleaseManifest requires commit identity for every release', () => {
  assert.throws(() => createReleaseManifest([
    { tag_name: 'v0.1.0', created_at: '2026-09-01T00:00:00Z', html_url: 'https://github.com/gesta-run/subpool/releases/tag/v0.1.0' },
  ]), /Missing commit SHA/)
})

test('githubHeaders adds authorization only when a token is provided', () => {
  assert.equal(githubHeaders().Authorization, undefined)
  assert.equal(githubHeaders('test-token').Authorization, 'Bearer test-token')
})
