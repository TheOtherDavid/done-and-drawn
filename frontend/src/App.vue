<template>
  <div class="app-shell">
    <header class="topbar">
      <a class="brand" href="#" @click.prevent="activeView = 'tasks'">
        <span class="brand-mark">✳</span>
        <span>Done & Drawn</span>
      </a>
      <nav class="main-nav" aria-label="Main navigation">
        <button :class="{ active: activeView === 'tasks' }" @click="activeView = 'tasks'">My tasks</button>
        <button :class="{ active: activeView === 'gallery' }" @click="openGallery">Reward gallery</button>
        <button :class="{ active: activeView === 'settings' }" @click="openSettings">Character settings</button>
      </nav>
    </header>

    <main class="page-wrap">
      <section v-if="activeView === 'tasks'" class="tasks-view">
        <div class="hero-row">
          <div>
            <p class="eyebrow">TODAY</p>
            <h1>Unlock Your Productivity! Unlock a Reward!</h1>
            <p class="subtitle">Complete a task to see it visualized.</p>
          </div>
          <div class="day-card"><span class="day-spark">✦</span><span>Start with one.</span></div>
        </div>

        <div v-if="pageError" class="notice error-notice" role="alert">{{ pageError }}</div>

        <div class="task-layout">
          <section class="task-column">
            <div class="section-heading">
              <div><p class="eyebrow">YOUR TASKS</p><h2>Tasks <span class="count-pill">{{ openTasks.length }}</span></h2></div>
              <button v-if="completedTasks.length" class="quiet-button" @click="showCompleted = !showCompleted">
                {{ showCompleted ? 'Hide completed' : `Show completed · ${completedTasks.length}` }}
              </button>
            </div>

            <div v-if="openTasks.length" class="task-list">
              <article v-for="task in openTasks" :key="task.id" class="task-card">
                <button class="check-button" :aria-label="`Complete ${task.title}`" :disabled="busyTaskId === task.id" @click="completeTask(task)">
                  <span v-if="busyTaskId === task.id" class="tiny-spinner"></span><span v-else></span>
                </button>
                <div class="task-copy">
                  <input v-if="editingId === task.id" v-model="editTitle" class="inline-edit" @keydown.enter.prevent="saveTask(task)" @keydown.esc="editingId = null" />
                  <h3 v-else>{{ task.title }}</h3>
                  <textarea v-if="editingId === task.id" v-model="editDescription" class="inline-edit description-edit" rows="2" placeholder="Add a note (optional)"></textarea>
                  <p v-else-if="task.description" class="task-description">{{ task.description }}</p>
                  <p v-else class="task-date">Added {{ formatDate(task.created_at) }}</p>
                </div>
                <div class="task-actions">
                  <template v-if="editingId === task.id">
                    <button class="icon-action save-action" aria-label="Save task" @click="saveTask(task)">Save</button>
                    <button class="icon-action" aria-label="Cancel editing" @click="editingId = null">Cancel</button>
                  </template>
                  <template v-else>
                    <button class="icon-action" aria-label="Edit task" @click="startEdit(task)">Edit</button>
                    <button class="icon-action delete-action" aria-label="Delete task" @click="deleteTask(task)">Delete</button>
                  </template>
                </div>
              </article>
            </div>
            <div v-else class="empty-list">
              <div class="empty-mark">✧</div>
              <h3>Your list is clear.</h3>
              <p>Add the next task.</p>
            </div>

            <div v-if="showCompleted && completedTasks.length" class="completed-section">
              <div class="section-heading completed-heading"><div><p class="eyebrow">COMPLETED TASKS</p><h2>Completed</h2></div></div>
              <div class="completed-day-groups">
                <section v-for="(group, index) in completedTaskGroups" :key="group.key" class="completed-day">
                  <button
                    class="completed-day-toggle"
                    :aria-expanded="isCompletedDayExpanded(group, index)"
                    :aria-controls="`completed-day-${group.key}`"
                    @click="toggleCompletedDay(group.key, index)"
                  >
                    <span class="completed-day-title">
                      <span class="completed-day-label">{{ group.label }}</span>
                      <span class="count-pill">{{ group.tasks.length }}</span>
                    </span>
                    <span class="completed-day-chevron" :class="{ expanded: isCompletedDayExpanded(group, index) }" aria-hidden="true">⌄</span>
                  </button>
                  <div v-show="isCompletedDayExpanded(group, index)" :id="`completed-day-${group.key}`" class="completed-day-tasks">
                    <article v-for="task in group.tasks" :key="task.id" class="task-card completed-card">
                      <span class="done-check">✓</span>
                      <div class="task-copy">
                        <h3>{{ task.title }}</h3>
                        <p class="task-date">Finished {{ formatDate(task.completed_at) }}</p>
                      </div>
                      <button v-if="task.reward_image_url" class="reward-thumb-button" :aria-label="`View reward for ${task.title}`" @click="openReward(task)">
                        <img :src="task.reward_image_url" alt="Task reward" class="reward-thumb" loading="lazy" decoding="async" />
                      </button>
                      <button v-else-if="task.reward_status === 'failed'" class="text-link" @click="openReward(task)">Reward failed · View</button>
                      <button v-else class="text-link" @click="openReward(task)">View reward</button>
                    </article>
                  </div>
                </section>
              </div>
            </div>
          </section>

          <aside class="add-panel">
            <p class="eyebrow">UP NEXT</p>
            <h2>Add a task</h2>
            <form @submit.prevent="createTask">
              <label for="task-title">What needs to get done?</label>
              <input id="task-title" v-model="newTitle" class="field" maxlength="300" required placeholder="e.g. Water the plants" />
              <label for="task-description">Details <span class="optional">Optional</span></label>
              <textarea id="task-description" v-model="newDescription" class="field textarea-field" rows="3" maxlength="5000" placeholder="Add details that will help you finish it"></textarea>
              <button class="primary-button full-button" type="submit" :disabled="savingTask || !newTitle.trim()">
                {{ savingTask ? 'Saving…' : 'Add task' }} <span>↗</span>
              </button>
            </form>
          </aside>
        </div>
      </section>

      <section v-else-if="activeView === 'gallery'" class="gallery-view">
        <div class="settings-heading gallery-heading">
          <button class="back-link" @click="activeView = 'tasks'">← Back to tasks</button>
          <p class="eyebrow">COMPLETED REWARDS</p>
          <h1>Reward gallery</h1>
          <p class="subtitle">Revisit every completed reward and download the original image.</p>
        </div>
        <div v-if="galleryError" class="notice error-notice" role="alert">
          {{ galleryError }}
          <button class="text-link" @click="openGallery">Try again</button>
        </div>
        <div v-else-if="galleryLoading" class="gallery-state" role="status" aria-live="polite">
          <span class="tiny-spinner"></span><span>Loading rewards…</span>
        </div>
        <div v-else-if="!galleryRewards.length" class="gallery-empty">
          <div class="empty-mark">✧</div>
          <h2>No rewards yet</h2>
          <p>Complete a task to add its reward image here.</p>
        </div>
        <div v-else class="gallery-grid" aria-label="Reward images">
          <article v-for="task in galleryRewards" :key="task.id" class="gallery-card">
            <button class="gallery-image-button" :aria-label="`Open full-size reward for ${task.title}`" :disabled="!!galleryImageErrors[task.id]" @click="openReward(task)">
              <img
                v-if="!galleryImageErrors[task.id]"
                :src="task.reward_image_url"
                :alt="`Reward image for ${task.title}`"
                loading="lazy"
                decoding="async"
                @error="markGalleryImageError(task.id)"
              />
              <span v-else class="gallery-image-error" role="status">Image unavailable</span>
              <span v-if="!galleryImageErrors[task.id]" class="gallery-image-open">Open full size</span>
            </button>
            <div class="gallery-card-details">
              <div class="gallery-card-copy">
                <h2>{{ task.title }}</h2>
                <p>{{ formatFullDate(task.completed_at) }}</p>
              </div>
              <a v-if="!galleryImageErrors[task.id]" class="secondary-button gallery-download" :href="rewardDownloadURL(task)" download>Download</a>
              <span v-else class="gallery-unavailable">Unavailable</span>
            </div>
          </article>
        </div>
      </section>

      <section v-else class="settings-view">
        <div class="settings-heading">
          <button class="back-link" @click="activeView = 'tasks'">← Back to tasks</button>
          <p class="eyebrow">REWARD SETTINGS</p>
          <h1>Character settings</h1>
          <p class="subtitle">Edit the character and visual style used in rewards.</p>
        </div>
        <div v-if="settingsError" class="notice error-notice" role="alert">{{ settingsError }}</div>
        <div v-if="settingsMessage" class="notice success-notice" role="status">{{ settingsMessage }}</div>

        <div class="settings-content">
          <div class="prompt-grid">
            <section v-for="field in promptFields" :key="field.key" class="settings-panel prompt-panel">
              <label :for="`prompt-${field.key}`">{{ field.label }}</label>
              <textarea :id="`prompt-${field.key}`" v-model="settings[field.key]" class="field prompt-field" rows="3" maxlength="12000" :placeholder="field.placeholder"></textarea>
            </section>
          </div>
          <div class="form-footer prompt-actions">
            <span class="save-state">{{ settings.can_generate ? 'Ready to generate rewards' : 'Add a reference image to enable rewards' }}</span>
            <button class="primary-button" :disabled="savingSettings" @click="saveSettings">{{ savingSettings ? 'Saving…' : 'Save changes' }}</button>
          </div>
          <section class="settings-panel references-panel">
            <div class="reference-header">
              <div><p class="eyebrow">REFERENCE IMAGES</p><h2>Character references</h2></div>
              <label class="upload-button" :class="{ disabled: uploading }">
                {{ uploading ? 'Uploading…' : '+ Add images' }}
                <input type="file" accept="image/png,image/jpeg,image/webp" multiple :disabled="uploading" @change="uploadReferences" />
              </label>
            </div>
            <p class="panel-intro">Choose one canonical image. Add up to 16 references, with a 32 MiB combined limit. Add an image to replace a missing reference file.</p>
            <div v-if="settings.references.length" class="reference-grid">
              <article v-for="reference in settings.references" :key="reference.id" class="reference-card" :class="{ canonical: reference.is_canonical }">
                <img :src="reference.image_url" alt="Character reference" />
                <div class="reference-card-footer">
                  <span v-if="reference.is_canonical" class="canonical-badge">Canonical</span>
                  <button v-else class="text-link" @click="chooseCanonical(reference)">Make canonical</button>
                  <button class="remove-ref" :aria-label="'Remove reference image'" :disabled="settings.references.length <= 1" @click="removeReference(reference)">×</button>
                </div>
              </article>
            </div>
            <div v-else class="upload-empty"><span>▧</span><p>No reference images yet</p><small>PNG, JPEG, or WebP · up to 15 MiB and 20 MP each</small></div>
            <p class="api-note" :class="{ ready: settings.can_generate }">
              <span class="status-dot"></span>{{ settings.can_generate ? 'Backend image generation is configured.' : 'A backend API key and canonical reference image are required.' }}
            </p>
          </section>
        </div>
      </section>
    </main>

    <footer class="footer"><span>Plan the day. Finish the work.</span><span>✦</span></footer>

    <div v-if="selectedTask" class="modal-backdrop" @click.self="closeReward">
      <section class="reward-modal" :class="{ 'gallery-reward-modal': activeView === 'gallery' }" role="dialog" aria-modal="true" aria-labelledby="reward-title">
        <button class="modal-close" aria-label="Close reward" @click="closeReward">×</button>
        <template v-if="selectedTask.reward_status === 'queued' || selectedTask.reward_status === 'generating'">
          <div class="preparing-art"><span class="orbit orbit-one"></span><span class="orbit orbit-two"></span><span class="preparing-star">✦</span></div>
          <p class="eyebrow">REWARD GENERATING</p>
          <h2 id="reward-title">Generating your reward…</h2>
          <p class="reward-copy">Reward for completing <strong>{{ selectedTask.title }}</strong>.</p>
          <div class="loading-track"><span></span></div>
        </template>
        <template v-else-if="selectedTask.reward_status === 'ready' && selectedTask.reward_image_url">
          <p class="eyebrow">TASK COMPLETE</p>
          <h2 id="reward-title">You got it done.</h2>
          <p class="reward-copy">Reward for completing <strong>{{ selectedTask.title }}</strong>.</p>
          <img :src="selectedTask.reward_image_url" alt="Your character celebrating your completed task" class="reward-image" />
          <div class="reward-modal-actions">
            <a class="secondary-button" :href="rewardDownloadURL(selectedTask)" download>Download original</a>
            <button class="primary-button modal-done" @click="closeReward">Keep going</button>
          </div>
        </template>
        <template v-else>
          <div class="failed-art">✧</div>
          <p class="eyebrow">GENERATION FAILED</p>
          <h2 id="reward-title">Reward image failed.</h2>
          <p class="reward-copy">{{ selectedTask.reward_error || 'The image could not be prepared just now.' }}</p>
          <button class="primary-button modal-done" :disabled="retrying" @click="retryReward(selectedTask)">{{ retrying ? 'Retrying…' : 'Retry generation' }}</button>
        </template>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { jsonOptions, request } from './api'

const tasks = ref([])
const galleryRewards = ref([])
const galleryImageErrors = ref({})
const activeView = ref('tasks')
const pageError = ref('')
const galleryError = ref('')
const galleryLoading = ref(false)
const settingsError = ref('')
const settingsMessage = ref('')
const showCompleted = ref(true)
const selectedTaskId = ref(null)
const busyTaskId = ref('')
const savingTask = ref(false)
const savingSettings = ref(false)
const uploading = ref(false)
const retrying = ref(false)
const newTitle = ref('')
const newDescription = ref('')
const editingId = ref('')
const editTitle = ref('')
const editDescription = ref('')
const settings = ref({ appearance: '', clothing: '', home: '', companion: '', personality: '', art_style: '', references: [], can_generate: false })
const promptFields = [
  { key: 'appearance', label: 'Appearance', placeholder: 'Shape, colors, markings, and other defining features.' },
  { key: 'clothing', label: 'Clothing', placeholder: 'Usual outfits, accessories, and clothing details.' },
  { key: 'home', label: 'Home', placeholder: 'Home, favorite settings, or familiar surroundings.' },
  { key: 'companion', label: 'Companion', placeholder: 'Companion character and how they appear together.' },
  { key: 'personality', label: 'Personality', placeholder: 'Temperament, expressions, and mannerisms.' },
  { key: 'art_style', label: 'Art style', placeholder: 'Medium, palette, lighting, and illustration style.' },
]
let pollTimer

const openTasks = computed(() => tasks.value.filter((task) => !task.completed_at))
const completedTasks = computed(() => tasks.value.filter((task) => task.completed_at))
const completedTaskGroups = computed(() => {
  const groups = new Map()
  for (const task of completedTasks.value) {
    const key = completedDayKey(task.completed_at)
    if (!groups.has(key)) groups.set(key, { key, label: formatCompletedDay(key), tasks: [] })
    groups.get(key).tasks.push(task)
  }
  return [...groups.values()]
    .sort((first, second) => {
      if (first.key === 'unknown') return 1
      if (second.key === 'unknown') return -1
      return second.key.localeCompare(first.key)
    })
    .map((group) => ({
      ...group,
      tasks: group.tasks.sort((first, second) => new Date(second.completed_at) - new Date(first.completed_at)),
    }))
})
const selectedTask = computed(() => {
  const task = tasks.value.find((item) => item.id === selectedTaskId.value)
  const galleryTask = galleryRewards.value.find((item) => item.id === selectedTaskId.value)
  return activeView.value === 'gallery' ? galleryTask || task || null : task || galleryTask || null
})
const expandedCompletedDays = ref({})

function isCompletedDayExpanded(group, index) {
  return Object.hasOwn(expandedCompletedDays.value, group.key) ? expandedCompletedDays.value[group.key] : index === 0
}

function toggleCompletedDay(key, index) {
  expandedCompletedDays.value = {
    ...expandedCompletedDays.value,
    [key]: !isCompletedDayExpanded({ key }, index),
  }
}

async function loadTasks() {
  try {
    tasks.value = await request('/api/tasks')
    pageError.value = ''
  } catch (error) {
    pageError.value = error.message
  }
}

async function refreshSettings() {
  settings.value = await request('/api/settings')
}

async function openSettings() {
  activeView.value = 'settings'
  settingsError.value = ''
  settingsMessage.value = ''
  try {
    await refreshSettings()
  } catch (error) {
    settingsError.value = error.message
  }
}

async function openGallery() {
  activeView.value = 'gallery'
  galleryError.value = ''
  galleryLoading.value = true
  galleryImageErrors.value = {}
  try {
    galleryRewards.value = await request('/api/rewards')
  } catch (error) {
    galleryError.value = error.message
  } finally {
    galleryLoading.value = false
  }
}

function markGalleryImageError(id) {
  galleryImageErrors.value = { ...galleryImageErrors.value, [id]: true }
}

async function createTask() {
  if (!newTitle.value.trim()) return
  savingTask.value = true
  pageError.value = ''
  try {
    const task = await request('/api/tasks', jsonOptions('POST', { title: newTitle.value, description: newDescription.value }))
    tasks.value = [task, ...tasks.value]
    newTitle.value = ''
    newDescription.value = ''
  } catch (error) {
    pageError.value = error.message
  } finally {
    savingTask.value = false
  }
}

function startEdit(task) {
  editingId.value = task.id
  editTitle.value = task.title
  editDescription.value = task.description || ''
}

async function saveTask(task) {
  if (!editTitle.value.trim()) return
  try {
    const updated = await request(`/api/tasks/${task.id}`, jsonOptions('PATCH', { title: editTitle.value, description: editDescription.value }))
    replaceTask(updated)
    editingId.value = ''
  } catch (error) {
    pageError.value = error.message
  }
}

async function deleteTask(task) {
  if (!window.confirm(`Delete “${task.title}”?`)) return
  try {
    await request(`/api/tasks/${task.id}`, { method: 'DELETE' })
    tasks.value = tasks.value.filter((item) => item.id !== task.id)
  } catch (error) {
    pageError.value = error.message
  }
}

async function completeTask(task) {
  busyTaskId.value = task.id
  pageError.value = ''
  try {
    const completed = await request(`/api/tasks/${task.id}/complete`, { method: 'POST' })
    replaceTask(completed)
    selectedTaskId.value = task.id
    showCompleted.value = true
  } catch (error) {
    pageError.value = error.message
  } finally {
    busyTaskId.value = ''
  }
}

async function retryReward(task) {
  retrying.value = true
  try {
    const updated = await request(`/api/tasks/${task.id}/retry`, { method: 'POST' })
    replaceTask(updated)
  } catch (error) {
    pageError.value = error.message
    closeReward()
  } finally {
    retrying.value = false
  }
}

function replaceTask(task) {
  const index = tasks.value.findIndex((item) => item.id === task.id)
  if (index < 0) tasks.value = [task, ...tasks.value]
  else tasks.value.splice(index, 1, task)
}

function openReward(task) { selectedTaskId.value = task.id }
function closeReward() { selectedTaskId.value = null }
function rewardDownloadURL(task) { return `/api/rewards/${encodeURIComponent(task.id)}/download` }

async function saveSettings() {
  savingSettings.value = true
  settingsMessage.value = ''
  settingsError.value = ''
  try {
    settings.value = await request('/api/settings', jsonOptions('PUT', {
      appearance: settings.value.appearance,
      clothing: settings.value.clothing,
      home: settings.value.home,
      companion: settings.value.companion,
      personality: settings.value.personality,
      art_style: settings.value.art_style,
    }))
    settingsMessage.value = 'Character settings saved.'
  } catch (error) {
    settingsError.value = error.message
  } finally {
    savingSettings.value = false
  }
}

async function uploadReferences(event) {
  const files = Array.from(event.target.files || [])
  event.target.value = ''
  if (!files.length) return
  uploading.value = true
  settingsError.value = ''
  settingsMessage.value = ''
  try {
    for (const file of files) {
      const body = new FormData()
      body.append('image', file)
      await request('/api/settings/references', { method: 'POST', body })
    }
    await refreshSettings()
    settingsMessage.value = 'Reference images added.'
  } catch (error) {
    settingsError.value = error.message
    await refreshSettings().catch(() => {})
  } finally {
    uploading.value = false
  }
}

async function chooseCanonical(reference) {
  try {
    settings.value = await request(`/api/settings/references/${reference.id}/canonical`, { method: 'PUT' })
    settingsMessage.value = 'Canonical image updated.'
  } catch (error) {
    settingsError.value = error.message
  }
}

async function removeReference(reference) {
  if (settings.value.references.length <= 1) return
  try {
    await request(`/api/settings/references/${reference.id}`, { method: 'DELETE' })
    await refreshSettings()
    settingsMessage.value = 'Reference image removed.'
  } catch (error) {
    settingsError.value = error.message
  }
}

function formatDate(value) {
  if (!value) return ''
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(new Date(value))
}

function formatFullDate(value) {
  if (!value) return 'Date unavailable'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Date unavailable'
  return new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'long', day: 'numeric' }).format(date)
}

function completedDayKey(value) {
  if (!value) return 'unknown'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'unknown'
  return [date.getFullYear(), String(date.getMonth() + 1).padStart(2, '0'), String(date.getDate()).padStart(2, '0')].join('-')
}

function formatCompletedDay(key) {
  if (key === 'unknown') return 'Date unavailable'
  const [year, month, day] = key.split('-').map(Number)
  return new Intl.DateTimeFormat(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' }).format(new Date(year, month - 1, day))
}

onMounted(() => {
  loadTasks()
  pollTimer = window.setInterval(() => {
    if (tasks.value.some((task) => task.reward_status === 'queued' || task.reward_status === 'generating')) loadTasks()
  }, 2000)
})

onBeforeUnmount(() => window.clearInterval(pollTimer))
</script>
