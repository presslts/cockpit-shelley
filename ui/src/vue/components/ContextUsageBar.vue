<!-- The token-count segment of the status readout ("15k"). Opens token/cost
     graphs and manual compaction actions. The count warms up at 100k / 200k /
     300k tokens (or 70/80/90% of a known context window) and the popup
     auto-opens once per browser at the first step. -->
<template>
  <div ref="barRef" class="context-usage-root">
    <Popover
      ref="popoverRef"
      :pt="{
        root: {
          class: 'chat-context-popup',
          id: popupId,
          'aria-label': 'Context usage',
        },
        content: { class: 'chat-context-popup-content' },
      }"
      @show="onPopupShow"
      @hide="popupOpen = false"
    >
      {{ formatTokenCount(contextWindowSize) }} tokens in the latest context
      <div v-if="popupOpen" class="usage-graph-panel">
        <div
          :class="{ 'usage-graph-panel-item-inactive': usageGraph !== 'cost' }"
          class="usage-graph-panel-item"
          :aria-hidden="usageGraph !== 'cost'"
          :inert="usageGraph !== 'cost'"
        >
          <TokenCostGraph
            :entries="usageEntries || []"
            :other-usage-rows="otherUsageRows || []"
            :conversation-id="conversationId"
            :active="usageGraph === 'cost'"
          >
            <template #mode-controls>
              <UsageGraphSwitch v-model="usageGraph" />
            </template>
          </TokenCostGraph>
        </div>
        <div
          :class="{ 'usage-graph-panel-item-inactive': usageGraph !== 'context' }"
          class="usage-graph-panel-item"
          :aria-hidden="usageGraph !== 'context'"
          :inert="usageGraph !== 'context'"
        >
          <ContextCompositionGraph :messages="messages || []">
            <template #mode-controls>
              <UsageGraphSwitch v-model="usageGraph" />
            </template>
          </ContextCompositionGraph>
        </div>
      </div>
      <div v-if="showLongConversationWarning" class="chat-popup-warning">
        This conversation is getting long.
        <br />
        Compact it or start a new conversation.
      </div>
      <div
        v-if="conversationId && (onDistillNewGeneration || onStartNewGeneration)"
        class="chat-distill-container"
      >
        <button
          v-if="onDistillNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleDistillNewGeneration"
        >
          {{ distilling ? "Compacting..." : "Compact Conversation" }}
        </button>
        <button
          v-if="onStartNewGeneration"
          :disabled="distilling"
          class="chat-distill-button chat-distill-generation-button"
          @click="handleStartNewGeneration"
        >
          Start New Generation
        </button>
      </div>
    </Popover>
    <button
      type="button"
      class="context-usage-label status-readout-control"
      :aria-label="usageTitle"
      aria-haspopup="dialog"
      :aria-expanded="popupOpen"
      :aria-controls="popupOpen ? popupId : undefined"
      v-tooltip.top="usageTooltip"
      @pointerenter="props.onUsageNeeded?.()"
      @focus="props.onUsageNeeded?.()"
      @click="openPopup($event)"
    >
      <span :class="['context-usage-label-tokens', 'status-readout-affordance', usageLevelClass]">{{
        formatTokenCount(contextWindowSize)
      }}</span>
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from "vue";
import Popover from "primevue/popover";
import type { Message } from "../../types";
import { contextUsageLevel, contextUsageLevelLabel } from "../../utils/contextUsage";
import { formatTokenCount } from "../../utils/tokenCostGraph";
import type { OtherUsageRow, UsageEntry } from "../../utils/tokenCostGraph";
import ContextCompositionGraph from "./ContextCompositionGraph.vue";
import TokenCostGraph from "./TokenCostGraph.vue";
import UsageGraphSwitch from "./UsageGraphSwitch.vue";

const props = defineProps<{
  contextWindowSize: number;
  /** Model context window from integration metadata or models.dev; 0 when unknown.
   *  Never displayed — only floors the warning color as the window fills. */
  maxContextTokens: number;
  conversationId?: string | null;
  usageEntries?: UsageEntry[];
  otherUsageRows?: OtherUsageRow[];
  messages?: Message[];
  onDistillNewGeneration?: () => Promise<void> | void;
  onStartNewGeneration?: () => Promise<void> | void;
  /** Called just before the popup opens. The parent computes usageEntries /
   *  otherUsageRows lazily (walking every message and parsing its usage data),
   *  so it needs a beat's warning; the graph renders empty for one tick and
   *  fills in on the next. */
  onUsageNeeded?: () => void;
  agentWorking?: boolean;
}>();

const distilling = ref(false);
const usageGraph = ref<"cost" | "context">("cost");
const popupOpen = ref(false);
const popupId = useId();
const popoverRef = ref<InstanceType<typeof Popover> | null>(null);
const barRef = ref<HTMLElement | null>(null);

// The token count is the whole warning: it warms up (amber, orange, red) as
// the conversation grows, rather than a triangle appearing beside it.
const usageLevel = computed(() =>
  contextUsageLevel(props.contextWindowSize, props.maxContextTokens),
);
const usageLevelClass = computed(() =>
  usageLevel.value ? `context-usage-label-tokens-${usageLevel.value}` : "",
);
// The popup's advice and its once-per-browser auto-open fire exactly when the
// count first colors.
const showLongConversationWarning = computed(() => usageLevel.value !== "");
let hasAutoOpened = false;

// Spelled out for the accessible name; the level is named in words too since
// hue alone is no signal for some readers.
const usageTitle = computed(() => {
  const level = contextUsageLevelLabel(usageLevel.value);
  const suffix = level ? ` — conversation ${level}` : "";
  return `Context usage: ${formatTokenCount(props.contextWindowSize)} tokens${suffix}`;
});
const usageTooltip = computed(() => `${usageTitle.value}. Click for details.`);

// Warn the parent as early as we can — hover/focus, which precede the click —
// so the usage walk has usually landed by the time the graph mounts.
function openPopup(event: Event) {
  props.onUsageNeeded?.();
  popoverRef.value?.toggle(event);
}

// Every path that makes the graph visible funnels through the Popover's show
// event, including the programmatic auto-open below, so ask again here: the
// hover/focus/click hints above are an optimization, this is the guarantee.
function onPopupShow() {
  popupOpen.value = true;
  props.onUsageNeeded?.();
}

// This component is not remounted on a conversation switch, but the parent
// resets its lazy usage gate on one, so a popup that was already open would
// keep showing the empty graph until dismissed and reopened. Re-ask.
watch(
  () => props.conversationId,
  () => {
    if (popupOpen.value) props.onUsageNeeded?.();
  },
);

// Auto-open popup once per browser at the long-conversation threshold.
// Programmatic open: PrimeVue's show() anchors to event.currentTarget, so pass
// the usage label element explicitly as the target.
watch(
  [showLongConversationWarning, () => props.agentWorking, () => props.conversationId],
  () => {
    const isMobile = window.innerWidth <= 768;
    if (
      showLongConversationWarning.value &&
      !props.agentWorking &&
      !isMobile &&
      props.conversationId &&
      !hasAutoOpened &&
      localStorage.getItem("shelley_long_convo_popup_shown") !== "1"
    ) {
      hasAutoOpened = true;
      // Wait a tick: with { immediate: true } this can fire before mount,
      // when barRef/popoverRef are still null. Only burn the once-per-browser
      // localStorage flag if the popup actually opens.
      void nextTick(() => {
        const anchor = barRef.value?.querySelector<HTMLElement>(".context-usage-label");
        if (!anchor || !popoverRef.value) return;
        localStorage.setItem("shelley_long_convo_popup_shown", "1");
        popoverRef.value.show(new Event("click"), anchor);
      });
    }
  },
  { immediate: true },
);

async function handleDistillNewGeneration() {
  if (distilling.value || !props.onDistillNewGeneration) return;
  distilling.value = true;
  try {
    await props.onDistillNewGeneration();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}

async function handleStartNewGeneration() {
  if (distilling.value || !props.onStartNewGeneration) return;
  distilling.value = true;
  try {
    await props.onStartNewGeneration();
    popoverRef.value?.hide();
  } finally {
    distilling.value = false;
  }
}
</script>
