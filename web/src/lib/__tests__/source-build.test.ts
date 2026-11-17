// Copyright (c) QuantumNous and contributors.
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterEach, beforeEach, expect, test } from 'vitest'

import { readSourceRevision } from '../../../scripts/source-revision'

let root: string
const git = (...args: string[]) =>
  execFileSync('git', args, {
    cwd: root,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'ignore'],
  }).trim()
beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), 'source-build-'))
  git('init', '--quiet')
  git(
    'remote',
    'add',
    'origin',
    'https://github.com/ztt928999373-ops/new-api.git'
  )
  writeFileSync(join(root, 'source.txt'), 'synthetic source\n')
  git('add', 'source.txt')
  git(
    '-c',
    'user.name=Synthetic',
    '-c',
    'user.email=synthetic@example.invalid',
    'commit',
    '--quiet',
    '-m',
    'synthetic source'
  )
})
afterEach(() => rmSync(root, { recursive: true, force: true }))

test('a clean modified-project checkout records its actual commit', () => {
  expect(readSourceRevision(root)).toBe(git('rev-parse', 'HEAD'))
})
test('tracked modifications cannot borrow the HEAD identity', () => {
  writeFileSync(join(root, 'source.txt'), 'modified source\n')
  expect(readSourceRevision(root)).toBe('UNKNOWN')
})
test('untracked build inputs cannot borrow the HEAD identity', () => {
  writeFileSync(join(root, 'additional.txt'), 'additional source\n')
  expect(readSourceRevision(root)).toBe('UNKNOWN')
})
test('an upstream-only checkout is not labelled as the modified project', () => {
  git(
    'remote',
    'set-url',
    'origin',
    'https://github.com/QuantumNous/new-api.git'
  )
  expect(readSourceRevision(root)).toBe('UNKNOWN')
})
test('a source archive without VCS and nested directories remain UNKNOWN', () => {
  mkdirSync(join(root, 'nested'))
  expect(readSourceRevision(join(root, 'nested'))).toBe('UNKNOWN')
  rmSync(join(root, '.git'), { recursive: true, force: true })
  expect(readSourceRevision(root)).toBe('UNKNOWN')
})
