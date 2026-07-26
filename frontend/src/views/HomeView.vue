<template>
  <!-- Custom Home Content: Full Page Mode -->
  <div v-if="homeContent" class="min-h-screen">
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page -->
  <div
    v-else
    class="home relative flex min-h-screen flex-col bg-[#f6f8f7] text-gray-900 dark:bg-[#070b0f] dark:text-white"
  >
    <div class="pointer-events-none absolute inset-0 overflow-hidden" aria-hidden="true">
      <div class="home-mesh absolute inset-0"></div>
    </div>

    <!-- Header -->
    <header class="relative z-20 px-5 py-4 sm:px-6">
      <nav class="mx-auto flex max-w-4xl items-center justify-between">
        <div class="flex items-center gap-2.5">
          <div
            class="flex h-9 w-9 items-center justify-center overflow-hidden rounded-xl bg-white shadow-sm ring-1 ring-black/5 dark:bg-dark-800 dark:ring-white/10"
          >
            <img :src="siteLogo || '/logo.svg'" alt="Logo" class="h-7 w-7 object-contain" />
          </div>
          <span class="text-sm font-semibold tracking-tight">{{ siteName }}</span>
        </div>

        <div class="flex items-center gap-1.5">
          <LocaleSwitcher />
          <button
            type="button"
            class="rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-800 dark:text-dark-400 dark:hover:bg-white/5 dark:hover:text-white"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            @click="toggleTheme"
          >
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>
          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="ml-1 inline-flex items-center rounded-full bg-gray-900 px-3.5 py-1.5 text-xs font-medium text-white transition hover:bg-gray-800 dark:bg-white dark:text-gray-900 dark:hover:bg-gray-100"
          >
            {{ t('home.dashboard') }}
          </router-link>
          <router-link
            v-else
            to="/login"
            class="ml-1 inline-flex items-center rounded-full bg-gray-900 px-3.5 py-1.5 text-xs font-medium text-white transition hover:bg-gray-800 dark:bg-white dark:text-gray-900 dark:hover:bg-gray-100"
          >
            {{ t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Main -->
    <main class="relative z-10 flex flex-1 flex-col items-center justify-center px-5 pb-16 pt-8 sm:px-6">
      <div class="mx-auto flex w-full max-w-xl flex-col items-center text-center">
        <div class="mb-6 animate-cat-float">
          <img
            src="/black-cat.png?v=cat2"
            :alt="t('home.mascot')"
            width="156"
            height="148"
            class="h-44 w-44 object-contain sm:h-56 sm:w-56 select-none"
            draggable="false"
            decoding="async"
          />
        </div>

        <h1 class="mb-3 text-4xl font-bold tracking-tight text-gray-950 dark:text-white sm:text-5xl">
          {{ siteName }}
        </h1>

        <p class="mb-8 max-w-md text-base leading-relaxed text-gray-600 dark:text-dark-300 sm:text-lg">
          {{ siteSubtitle }}
        </p>

        <router-link
          :to="primaryCtaPath"
          class="btn btn-primary group px-8 py-3 text-sm shadow-lg shadow-primary-500/25"
        >
          {{ primaryCtaLabel }}
          <Icon
            name="arrowRight"
            size="md"
            class="ml-1 transition-transform group-hover:translate-x-0.5"
            :stroke-width="2"
          />
        </router-link>

        <div class="mt-10 flex flex-wrap items-center justify-center gap-2">
          <span
            v-for="tag in featureTags"
            :key="tag"
            class="rounded-full border border-gray-200/80 bg-white/70 px-3 py-1 text-xs text-gray-600 dark:border-white/10 dark:bg-white/5 dark:text-dark-300"
          >
            {{ tag }}
          </span>
        </div>
      </div>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 px-5 py-6 sm:px-6">
      <div
        class="mx-auto flex max-w-4xl flex-col items-center justify-between gap-3 text-center sm:flex-row sm:text-left"
      >
        <p class="text-xs text-gray-500 dark:text-dark-400">
          &copy; {{ currentYear }} {{ siteName }}
        </p>
        <div class="flex items-center gap-4">
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-gray-500 transition-colors hover:text-gray-800 dark:text-dark-400 dark:hover:text-white"
          >
            {{ t('home.docs') }}
          </a>
          <a
            :href="githubUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-gray-500 transition-colors hover:text-gray-800 dark:text-dark-400 dark:hover:text-white"
          >
            GitHub
          </a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import { sanitizeUrl } from '@/utils/url'

const { t } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

const siteName = computed(
  () => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API'
)
const siteLogo = computed(() =>
  sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', {
    allowRelative: true,
    allowDataUrl: true
  })
)
const siteSubtitle = computed(
  () => appStore.cachedPublicSettings?.site_subtitle || t('home.heroSubtitle')
)
const docUrl = computed(() =>
  sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || '')
)
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')
const registrationEnabled = computed(
  () => appStore.cachedPublicSettings?.registration_enabled !== false
)

const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

const isDark = ref(document.documentElement.classList.contains('dark'))
const githubUrl = 'https://github.com/Wei-Shaw/sub2api'

const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => (isAdmin.value ? '/admin/dashboard' : '/dashboard'))

const primaryCtaPath = computed(() => {
  if (isAuthenticated.value) return dashboardPath.value
  return registrationEnabled.value ? '/register' : '/login'
})
const primaryCtaLabel = computed(() => {
  if (isAuthenticated.value) return t('home.goToDashboard')
  if (registrationEnabled.value) return t('home.getStarted')
  return t('home.login')
})

const currentYear = computed(() => new Date().getFullYear())

const featureTags = computed(() => [
  t('home.tags.subscriptionToApi'),
  t('home.tags.stickySession'),
  t('home.tags.realtimeBilling')
])

function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

function initTheme() {
  const savedTheme = localStorage.getItem('theme')
  if (
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  ) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }
}

onMounted(() => {
  initTheme()
  authStore.checkAuth()
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})
</script>

<style scoped>
.home-mesh {
  background: radial-gradient(
    ellipse 55% 40% at 50% 35%,
    rgba(20, 184, 166, 0.12),
    transparent 70%
  );
}

:global(.dark) .home-mesh {
  background: radial-gradient(
    ellipse 55% 40% at 50% 35%,
    rgba(20, 184, 166, 0.1),
    transparent 70%
  );
}

</style>
