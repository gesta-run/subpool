import { readdir, readFile, writeFile } from 'node:fs/promises'
import { extname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzip } from 'node:zlib'
import { promisify } from 'node:util'

const compress = promisify(gzip)
const compressible = new Set(['.css', '.html', '.js', '.json', '.svg'])

async function precompress(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  await Promise.all(entries.map(async (entry) => {
    const filename = join(directory, entry.name)
    if (entry.isDirectory()) {
      await precompress(filename)
      return
    }
    if (!compressible.has(extname(entry.name))) {
      return
    }
    const source = await readFile(filename)
    if (source.length < 1024) {
      return
    }
    const compressed = await compress(source, { level: 9 })
    if (compressed.length < source.length) {
      await writeFile(`${filename}.gz`, compressed)
    }
  }))
}

await precompress(fileURLToPath(new URL('../dist/assets', import.meta.url)))
