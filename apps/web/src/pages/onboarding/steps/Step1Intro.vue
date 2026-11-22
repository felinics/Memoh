<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useOnboarding } from '@/composables/useOnboarding'
import { nextFrame } from '../useStepTransition'
import StepExitShell from '../components/step-exit-shell.vue'
import { prefersReducedMotion } from '@/lib/runtime-env'
import { safeLocalGet, safeLocalSet } from '@/utils/safe-storage'
import { ONBOARDING_KEYS } from '../constants'

const { t } = useI18n()
const { nextStep, introTextVisible } = useOnboarding()

const exiting = ref(false)
const isSkipped = ref(false)
const showContent = ref(false)

watch(showContent, (v) => {
  if (v) introTextVisible.value = true
})

interface Bubble {
  id: number
  startX: number
  startY: number
  currentX: number
  currentY: number
  scale: number
  opacity: number
  delay: number
}

function handleStart() {
  exiting.value = true
  setTimeout(() => nextStep(), 175)
}
const bubbles = ref<Bubble[]>([])
let animationFrameId = 0
let startTime = 0

let frameCount = 0
let lastFpsTime = 0
let isDegraded = false

onMounted(() => {
  if (prefersReducedMotion()) {
    skipAnimation()
    return
  }
  if (safeLocalGet(ONBOARDING_KEYS.introSeen) === '1') {
    isSkipped.value = true
    nextFrame(() => {
      showContent.value = true
    })
    return
  }
  initBubbles()
  startTime = performance.now()
  lastFpsTime = startTime
  animate()
})

onUnmounted(() => {
  cancelAnimationFrame(animationFrameId)
})

const initBubbles = (count = window.innerWidth < 768 ? 4 : 20) => {
  bubbles.value = Array.from({ length: count }).map((_, i) => {
    const angle = (Math.PI * 2 * i) / count + (Math.random() * 0.2)
    const radius = 300 + Math.random() * 150
    return {
      id: i,
      startX: Math.cos(angle) * radius,
      startY: Math.sin(angle) * radius,
      currentX: Math.cos(angle) * radius,
      currentY: Math.sin(angle) * radius,
      scale: 0,
      opacity: 0,
      delay: Math.random() * 1000,
    }
  })
}

const easeInOutCubic = (t: number) => t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2

const animate = () => {
  if (isSkipped.value) return

  const now = performance.now()
  const elapsed = now - startTime

  frameCount++
  if (now - lastFpsTime >= 1000) {
    const fps = frameCount
    if (fps < 30 && !isDegraded) {
      isDegraded = true
      bubbles.value = bubbles.value.slice(0, Math.ceil(bubbles.value.length / 2))
    }
    frameCount = 0
    lastFpsTime = now
  }

  let allFinished = true

  bubbles.value.forEach(b => {
    const bubbleElapsed = elapsed - b.delay
    if (bubbleElapsed < 0) return

    if (bubbleElapsed < 1000) {
      const progress = bubbleElapsed / 1000
      b.scale = easeInOutCubic(progress)
      b.opacity = progress
      allFinished = false
    } else if (bubbleElapsed < 4000) {
      const progress = (bubbleElapsed - 1000) / 3000
      const easeProgress = easeInOutCubic(progress)
      b.currentX = b.startX * (1 - easeProgress)
      b.currentY = b.startY * (1 - easeProgress)
      b.scale = 1 - (easeProgress * 0.8)
      b.opacity = 1 - easeProgress
      allFinished = false
    } else {
      b.opacity = 0
    }
  })

  if (allFinished && elapsed > 4000) {
    safeLocalSet(ONBOARDING_KEYS.introSeen, '1')
    if (!showContent.value) showContent.value = true
    setTimeout(() => { isSkipped.value = true }, 600)
  } else {
    if (elapsed > 3800 && !showContent.value) {
      showContent.value = true
    }
    animationFrameId = requestAnimationFrame(animate)
  }
}

const skipAnimation = () => {
  safeLocalSet(ONBOARDING_KEYS.introSeen, '1')
  isSkipped.value = true
  showContent.value = true
  cancelAnimationFrame(animationFrameId)
}
</script>

<template>
  <StepExitShell :exiting="exiting">
    <div class="relative w-full flex flex-col items-center justify-center">
      <!-- Animation stage -->
      <div class="relative flex items-center justify-center w-full h-[100px]">
        <!-- Memoh Logo -->
        <div
          class="absolute z-(--z-sticky) flex items-center justify-center w-24 h-24 bg-background rounded-2xl border border-border transition-all duration-1000 shadow-none"
          :class="showContent ? 'scale-100' : 'scale-110 ring-2 ring-primary/20'"
        >
          <img
            src="/logo.svg"
            alt="Memoh"
            class="w-[60px] h-[60px] object-contain"
          >
        </div>

        <!-- Bubbles -->
        <div
          v-if="!isSkipped"
          class="absolute inset-0 pointer-events-none"
        >
          <div
            v-for="b in bubbles"
            :key="b.id"
            class="absolute top-1/2 left-1/2 will-change-transform -ml-[75px] -mt-[24px]"
            :style="{
              transform: `translate(${b.currentX}px, ${b.currentY}px) scale(${b.scale})`,
              opacity: b.opacity,
            }"
          >
            <div class="flex items-center gap-3 p-3 rounded-[10px] bg-background border border-border shadow-lg w-[150px]">
              <div class="w-6 h-6 rounded-md bg-muted text-muted-foreground shrink-0 flex items-center justify-center">
                <svg
                  class="w-4 h-4"
                  fill="currentColor"
                  viewBox="0 0 24 24"
                ><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.41 0-8-3.59-8-8s3.59-8 8-8 8 3.59 8 8-3.59 8-8 8zm-1-13h2v6h-2zm0 8h2v2h-2z" /></svg>
              </div>
              <div class="flex flex-col gap-1.5 w-full">
                <div class="w-12 h-2 bg-muted rounded-full" />
                <div class="w-20 h-2 bg-muted/60 rounded-full" />
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Text + CTA -->
      <div class="flex flex-col items-center text-center px-4 w-full max-w-[800px] mt-8">
        <h2
          class="ml-1 font-semibold text-3xl md:text-5xl text-foreground tracking-tight leading-tight transition-all duration-500 ease-out delay-[60ms]"
          :class="showContent ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'"
        >
          {{ t('onboarding.intro.title') }}
        </h2>
        <p
          class="mt-4 text-muted-foreground text-sm md:text-base max-w-[480px] leading-relaxed font-light whitespace-pre-line transition-all duration-500 ease-out delay-[120ms]"
          :class="showContent ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'"
        >
          {{ t('onboarding.intro.description') }}
        </p>
        <div
          class="mt-6 transition-all duration-500 ease-out delay-[180ms]"
          :class="showContent ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4'"
        >
          <button
            class="inline-flex h-[2.625rem] w-[240px] items-center justify-center rounded-lg bg-primary px-5 font-normal text-primary-foreground shadow-none ring-[0.8px] ring-primary ring-offset-2 ring-offset-background transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
            @click="handleStart"
          >
            {{ t('onboarding.start') }}
          </button>
        </div>
      </div>
    </div>
  </StepExitShell>
</template>
