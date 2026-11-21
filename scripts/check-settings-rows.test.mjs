import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { checkSettingsRows } from './check-settings-rows.mjs'

const scriptPath = fileURLToPath(new URL('./check-settings-rows.mjs', import.meta.url))

function runFixture(t, files) {
  const dir = mkdtempSync(join(tmpdir(), 'memoh-settings-rows-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  for (const [relPath, content] of Object.entries(files)) {
    const full = join(dir, relPath)
    mkdirSync(dirname(full), { recursive: true })
    writeFileSync(full, content)
  }
  return { dir, ...checkSettingsRows(dir) }
}

test('accepts rows in SettingsSection, SettingsList in dialogs, bleeds in cards, and own-surface blocks', (t) => {
  const { errors } = runFixture(t, {
    'pages/clean.vue': `
      <template>
        <SettingsSection title="General">
          <SettingsRow label="Name" />
          <ModelListRow label="Sonnet" />
          <SettingsBleed>
            <div class="px-(--settings-inset) py-3">Edge to edge</div>
          </SettingsBleed>
          <div class="rounded-md bg-muted px-4 py-3">Own surface</div>
          <Dialog>
            <DialogPanel>
              <DialogBody>
                <SettingsList>
                  <SettingsRow label="Dialog item" />
                </SettingsList>
              </DialogBody>
            </DialogPanel>
          </Dialog>
        </SettingsSection>
      </template>
    `,
  })
  assert.deepEqual(errors, [])
})

test('flags loose rows in dialogs and enforces reason length on ui-allow-loose-row', (t) => {
  const { errors } = runFixture(t, {
    'pages/dialog.vue': `
      <template>
        <Dialog>
          <DialogPanel>
            <DialogBody>
              <SettingsRow label="Collapsed" />
              <!-- ui-allow-loose-row: short -->
              <SettingsRow label="Too short reason" />
              <!-- ui-allow-loose-row: lone toggle in dialog body needs no hairline -->
              <SettingsRow label="Allowed lone row" />
            </DialogBody>
          </DialogPanel>
        </Dialog>
      </template>
    `,
  })
  assert.equal(errors.length, 2)
  assert.match(errors[0], /pages\/dialog\.vue:6\s+<SettingsRow> is a settings row outside any SettingsSection \/ SettingsList/)
  assert.match(errors[1], /pages\/dialog\.vue:8\s+<SettingsRow> is a settings row outside any SettingsSection \/ SettingsList/)
})

test('flags hand-written card insets directly and across component roots, while respecting escapes', (t) => {
  const { errors } = runFixture(t, {
    'components/leaf.vue': `
      <template>
        <p class="px-4 text-body">Indented twice</p>
      </template>
    `,
    'components/mid.vue': `
      <script setup lang="ts">
      import Leaf from './leaf.vue'
      </script>
      <template>
        <Leaf />
      </template>
    `,
    'pages/card.vue': `
      <script setup lang="ts">
      import Mid from '@/components/mid.vue'
      </script>
      <template>
        <SettingsSection title="Account">
          <SettingsRow class="mx-4" label="Legacy row margin" />
          <Mid />
          <!-- ui-allow-card-inset: intentional aligned custom block -->
          <Mid />
        </SettingsSection>
      </template>
    `,
  })
  assert.equal(errors.length, 2)
  assert.match(errors[0], /components\/leaf\.vue:3\s+<p> px-4 \(via components\/mid\.vue:6\) inside a settings card \(rendered by pages\/card\.vue:8\)/)
  assert.match(errors[1], /pages\/card\.vue:7\s+<SettingsRow> mx-4 inside a settings card/)
})

test('distinguishes SettingsList from SettingsSection for hand insets and card-only bleeds', (t) => {
  const { errors } = runFixture(t, {
    'components/bleed-child.vue': `
      <template>
        <SettingsBleed><div class="px-4">Table</div></SettingsBleed>
      </template>
    `,
    'pages/list-in-dialog.vue': `
      <script setup lang="ts">
      import BleedChild from '@/components/bleed-child.vue'
      </script>
      <template>
        <Dialog>
          <DialogContent>
            <SettingsList>
              <div class="px-4 py-2">Allowed outside a card</div>
              <ModelListRow label="Overflows dialog by 16px" />
              <BleedChild />
            </SettingsList>
          </DialogContent>
        </Dialog>
      </template>
    `,
  })
  assert.equal(errors.length, 2)
  assert.match(errors[0], /components\/bleed-child\.vue:3\s+<SettingsBleed> bleeds by --settings-inset outside any SettingsSection card \(rendered by pages\/list-in-dialog\.vue:11\)/)
  assert.match(errors[1], /pages\/list-in-dialog\.vue:10\s+<ModelListRow> bleeds by --settings-inset outside any SettingsSection card/)
})

test('models SettingsSection #actions and #footer slots accurately', (t) => {
  const { errors } = runFixture(t, {
    'pages/slots.vue': `
      <template>
        <SettingsSection title="Models">
          <template #actions>
            <div class="px-4">Header pill outside card</div>
            <SettingsRow label="Row in header toolbar" />
          </template>
          <SettingsRow label="Valid row" />
          <template #footer>
            <div class="px-4">Double-inset footer bar</div>
            <SettingsRow label="Row in footer bar" />
          </template>
        </SettingsSection>
      </template>
    `,
  })
  assert.equal(errors.length, 3)
  assert.match(errors[0], /pages\/slots\.vue:10\s+<div> px-4 inside a settings card/)
  assert.match(errors[1], /pages\/slots\.vue:11\s+<SettingsRow> is a settings row outside any SettingsSection \/ SettingsList/)
  assert.match(errors[2], /pages\/slots\.vue:6\s+<SettingsRow> is a settings row outside any SettingsSection \/ SettingsList/)
})

test('handles dynamic <component :is> list wrappers for plain tags and mixed section/list branches', (t) => {
  const { errors } = runFixture(t, {
    'pages/plain-branch.vue': `
      <template>
        <component :is="embedded ? 'div' : SettingsSection">
          <SettingsRow label="Loses rhythm on div branch" />
        </component>
      </template>
    `,
    'pages/mixed-branch.vue': `
      <template>
        <component :is="framed ? SettingsSection : SettingsList">
          <SettingsRow label="Valid on both branches" />
          <div class="-mx-4">Breaks framed branch</div>
          <ModelListRow label="Breaks unframed branch" />
        </component>
      </template>
    `,
  })
  assert.equal(errors.length, 3)
  assert.match(errors[0], /pages\/mixed-branch\.vue:5\s+<div> -mx-4 inside a settings card/)
  assert.match(errors[1], /pages\/mixed-branch\.vue:6\s+<ModelListRow> bleeds by --settings-inset outside any SettingsSection card/)
  assert.match(errors[2], /pages\/plain-branch\.vue:3\s+<component :is="embedded \? 'div' : SettingsSection"> renders a plain element/)
})

test('keeps in-place overlay triggers in parent context while resetting teleported content', (t) => {
  const { errors } = runFixture(t, {
    'components/tooltip-wrap.vue': `
      <template>
        <TooltipProvider v-if="tip">
          <Tooltip>
            <TooltipTrigger as-child>
              <slot />
            </TooltipTrigger>
            <TooltipContent>
              <SettingsRow label="Loose row inside tooltip bubble" />
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
        <slot v-else />
      </template>
    `,
    'pages/triggers.vue': `
      <script setup lang="ts">
      import TooltipWrap from '@/components/tooltip-wrap.vue'
      </script>
      <template>
        <SettingsSection title="Triggers">
          <TooltipWrap>
            <SettingsRow label="Still in SettingsSection via in-place trigger" />
          </TooltipWrap>
          <ConfirmPopover confirm-text="Delete" cancel-text="Cancel">
            <template #trigger>
              <div class="px-4">Trigger sits in card</div>
            </template>
            <SettingsRow label="Loose row inside confirm popover content" />
          </ConfirmPopover>
        </SettingsSection>
      </template>
    `,
  })
  assert.equal(errors.length, 3)
  assert.match(errors[0], /components\/tooltip-wrap\.vue:9\s+<SettingsRow> is a settings row outside any SettingsSection \/ SettingsList/)
  assert.match(errors[1], /pages\/triggers\.vue:12\s+<div> px-4 inside a settings card/)
  assert.match(errors[2], /pages\/triggers\.vue:14\s+<SettingsRow> is a settings row outside any SettingsSection \/ SettingsList/)
})

test('catches loose root rows in dynamic <component :is> tabs that are imported in script only', (t) => {
  const { errors } = runFixture(t, {
    'pages/bots/tab-loose.vue': `
      <template>
        <div>
          <SettingsRow label="Loose row in dynamic tab" />
        </div>
      </template>
    `,
    'pages/bots/detail.vue': `
      <script setup lang="ts">
      import TabLoose from './tab-loose.vue'
      const activeTab = TabLoose
      </script>
      <template>
        <component :is="activeTab" />
      </template>
    `,
  })
  assert.equal(errors.length, 1)
  assert.match(errors[0], /pages\/bots\/tab-loose\.vue:4\s+<SettingsRow> sits at the root of pages\/bots\/tab-loose\.vue/)
})

test('merges duplicate <slot> outlets across conditional branches conservatively', (t) => {
  const { errors } = runFixture(t, {
    'components/conditional-card.vue': `
      <template>
        <SettingsSection v-if="framed" title="Framed">
          <slot />
        </SettingsSection>
        <div v-else>
          <slot />
        </div>
      </template>
    `,
    'pages/caller.vue': `
      <script setup lang="ts">
      import ConditionalCard from '@/components/conditional-card.vue'
      </script>
      <template>
        <div>
          <ConditionalCard>
            <SettingsRow label="Unframed branch has no list" />
            <div class="px-4">Framed branch already insets</div>
          </ConditionalCard>
        </div>
      </template>
    `,
  })
  assert.equal(errors.length, 2)
  assert.match(errors[0], /pages\/caller\.vue:8\s+<SettingsRow> sits at the root of pages\/caller\.vue/)
  assert.match(errors[1], /pages\/caller\.vue:9\s+<div> px-4 inside a settings card/)
})

test('CLI exits 0 on valid tree and 1 on violations', (t) => {
  const ok = runFixture(t, {
    'page.vue': '<template><SettingsSection><SettingsRow label="OK" /></SettingsSection></template>',
  })
  const passRun = spawnSync(process.execPath, [scriptPath, ok.dir], { encoding: 'utf8' })
  assert.equal(passRun.status, 0, passRun.stderr)
  assert.match(passRun.stdout, /settings rows OK/)

  const bad = runFixture(t, {
    'page.vue': '<template><div><SettingsRow label="Loose" /></div></template>',
  })
  const failRun = spawnSync(process.execPath, [scriptPath, bad.dir], { encoding: 'utf8' })
  assert.equal(failRun.status, 1)
  assert.match(failRun.stderr, /settings rows: 1 problem/)
})
