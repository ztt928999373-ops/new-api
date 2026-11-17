// Copyright (c) QuantumNous and contributors.
import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'

// This is build-time Git evidence, not a VERSION/GITHUB_SHA/OCI label claim.
export function readSourceRevision(root: string): string {
  try {
    const git = (...args: string[]) =>
      execFileSync('git', args, {
        cwd: root,
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'ignore'],
      }).trim()
    if (resolve(git('rev-parse', '--show-toplevel')) !== resolve(root)) {
      return 'UNKNOWN'
    }
    const origin = git('remote', 'get-url', 'origin').replace(/\.git$/, '')
    if (
      ![
        'https://github.com/ztt928999373-ops/new-api',
        'git@github.com:ztt928999373-ops/new-api',
      ].includes(origin)
    ) {
      return 'UNKNOWN'
    }
    if (git('status', '--porcelain', '--untracked-files=normal')) {
      return 'UNKNOWN'
    }
    const revision = git('rev-parse', '--verify', 'HEAD')
    return /^[0-9a-f]{40}$/.test(revision) ? revision : 'UNKNOWN'
  } catch {
    return 'UNKNOWN'
  }
}
