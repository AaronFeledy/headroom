#!/usr/bin/env node
'use strict';

// Contender for Headroom's Claude Code refresh-lock interop fixtures. It
// reproduces Claude Code 2.1.325's two-lock sequence with the pinned
// proper-lockfile release: mkdir <configDir>/.oauth_refresh.lock
// (realpath: false), then mkdir <realpath(configDir)>.lock. A legacy ELOCKED
// releases the new lock and reports the failure.
//
// Commands (stdout markers, one per line):
//   hold <dir> <staleMs> <updateMs>
//       Acquire both locks, print LOCKED, wait for stdin to close, release
//       both, print RELEASED.
//   hold-stale <dir> <staleMs> <updateMs>
//       Acquire both locks and stop their mtime updates, like a hung holder.
//       Print LOCKED_STALE, wait for stdin to close, release both, print
//       RELEASED.
//   try <dir> <staleMs> <updateMs> <new|legacy|both>
//       Try once. Print ACQUIRED or ELOCKED:<lock>, releasing whatever it took.
//   check <dir> <staleMs> <new|legacy>
//       Print FRESH or STALE without acquiring.
//   clock
//       Print Date.now() so a caller on another kernel can measure clock skew.
//
// proper-lockfile clamps stale to at least 2000ms and update to at least
// 1000ms, so shorter values silently become those.

const fs = require('node:fs');
const path = require('node:path');
const lockfile = require('proper-lockfile');
// getLocks is not re-exported by the package index; the pinned internal module
// is the same instance the index uses, so its lock records are the live ones.
const lockfileInternals = require('proper-lockfile/lib/lockfile');

const [, , command, dirArg, staleArg, updateArg, whichArg] = process.argv;
const stale = Number(staleArg || 2000);
const update = Number(updateArg || 1000);
const which = whichArg || 'both';
const options = { realpath: false, stale, update, retries: 0 };

function say(line) {
  process.stdout.write(line + '\n');
}

function dir() {
  return path.resolve(dirArg);
}

function target(name) {
  if (name === 'legacy') {
    return fs.realpathSync(dir());
  }
  return path.join(dir(), '.oauth_refresh');
}

async function tryLock(name) {
  try {
    return { name, release: await lockfile.lock(target(name), options) };
  } catch (err) {
    if (err.code === 'ELOCKED') {
      return { name, blocked: true };
    }
    throw err;
  }
}

async function acquireBoth() {
  const held = [];
  for (const name of ['new', 'legacy']) {
    const result = await tryLock(name);
    if (result.blocked) {
      for (const release of held.reverse()) {
        await release();
      }
      return { blocked: name };
    }
    held.push(result.release);
  }
  return { releases: held };
}

async function releaseAll(releases) {
  for (const release of releases.reverse()) {
    await release();
  }
}

function waitForStdinClose() {
  return new Promise((resolve) => {
    process.stdin.on('end', resolve);
    process.stdin.on('close', resolve);
    process.stdin.resume();
  });
}

// stopUpdates simulates a hung holder: the lock directories stay owned by this
// process, but their mtimes stop advancing.
function stopUpdates() {
  const locks = lockfileInternals.getLocks();
  for (const key of Object.keys(locks)) {
    const lock = locks[key];
    if (lock.updateTimeout) {
      clearTimeout(lock.updateTimeout);
      lock.updateTimeout = null;
    }
    lock.lastUpdate = 0;
  }
}

async function main() {
  switch (command) {
    case 'hold':
    case 'hold-stale': {
      const { blocked, releases } = await acquireBoth();
      if (blocked) {
        say('ELOCKED:' + blocked);
        process.exit(2);
      }
      if (command === 'hold-stale') {
        stopUpdates();
        say('LOCKED_STALE');
      } else {
        say('LOCKED');
      }
      await waitForStdinClose();
      await releaseAll(releases);
      say('RELEASED');
      return;
    }
    case 'try': {
      const names = which === 'both' ? ['new', 'legacy'] : [which];
      const held = [];
      for (const name of names) {
        const result = await tryLock(name);
        if (result.blocked) {
          await releaseAll(held);
          say('ELOCKED:' + name);
          return;
        }
        held.push(result.release);
      }
      await releaseAll(held);
      say('ACQUIRED');
      return;
    }
    case 'check': {
      const fresh = await lockfile.check(target(which === 'legacy' ? 'legacy' : 'new'), options);
      say(fresh ? 'FRESH' : 'STALE');
      return;
    }
    case 'clock': {
      say(String(Date.now()));
      return;
    }
    default:
      throw new Error('unknown command: ' + command);
  }
}

main().catch((err) => {
  process.stderr.write(String((err && err.stack) || err) + '\n');
  process.exit(1);
});
