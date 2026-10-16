import assert from 'node:assert/strict'
import test from 'node:test'

import {
  clientVersionLine,
  compareSemVer,
  coreVersion,
  formatBytes,
  isAgentOutdated,
  linuxReleaseUpgradeAllowed,
  parseSemVer,
  runtimeLabel,
  statusLabel,
  taskStatusLabel,
} from '../.test-dist/nodeDisplay.js'

test('formatBytes handles boundaries and invalid telemetry', () => {
  assert.equal(formatBytes(), '—')
  assert.equal(formatBytes(0), '0 B')
  assert.equal(formatBytes(1024), '1.0 KB')
  assert.equal(formatBytes(5 * 1024 * 1024), '5.0 MB')
  assert.equal(formatBytes(Number.NaN), '0 B')
})

test('semver parser finds version tokens and compares prereleases', () => {
  assert.deepEqual(parseSemVer('agent v1.2.3'), {
    major: 1,
    minor: 2,
    patch: 3,
    pre: '',
    valid: true,
    raw: 'agent v1.2.3',
  })
  assert.equal(compareSemVer(parseSemVer('1.2.3-rc.1'), parseSemVer('1.2.3')), -1)
  assert.equal(compareSemVer(parseSemVer('1.10.0'), parseSemVer('1.9.9')), 1)
})

test('core version is separate from the wrapper and does not guess a platform', () => {
  assert.equal(coreVersion({ core_version: 'v0.15.12-3-g2ebfece', agent_version: 'v0.15.9' }), 'v0.15.12-3-g2ebfece')
  assert.equal(coreVersion({ agent_version: 'v0.15.9' }), 'v0.15.9')
  assert.equal(clientVersionLine({ client_type: 'android', client_version: '1.1.0' }), 'Android 1.1.0')
  assert.equal(clientVersionLine({ client_type: 'openwrt', client_version: '0.15.12-r2' }), 'OpenWrt 0.15.12-r2')
  assert.equal(clientVersionLine({ client_type: 'linux', client_version: 'v0.15.12' }), '')
  assert.equal(clientVersionLine({ agent_version: '1.1.0' }), '')
  assert.equal(linuxReleaseUpgradeAllowed(''), true)
  assert.equal(linuxReleaseUpgradeAllowed('linux'), true)
  assert.equal(linuxReleaseUpgradeAllowed('android'), false)
  assert.equal(linuxReleaseUpgradeAllowed('openwrt'), false)
  assert.equal(isAgentOutdated('1.1.0', 'v0.15.12'), false)
  assert.equal(isAgentOutdated('v0.15.9', 'v0.15.12'), true)
})

test('agent upgrade detection does not downgrade newer versions', () => {
  assert.equal(isAgentOutdated('v0.3.0', 'v0.4.0'), true)
  assert.equal(isAgentOutdated('v0.5.0', 'v0.4.0'), false)
  assert.equal(isAgentOutdated('unknown', 'v0.4.0'), true)
  assert.equal(isAgentOutdated('', ''), false)
})

test('status helpers expose stable Chinese labels', () => {
  assert.equal(statusLabel('unauthorized'), '鉴权失败')
  assert.equal(runtimeLabel('running'), '运行中')
  assert.equal(taskStatusLabel('partial'), '部分成功')
})
