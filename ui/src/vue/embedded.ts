import { createApp, type App as VueApp } from "vue";
import PrimeVue from "primevue/config";
import Tooltip from "primevue/tooltip";
import FocusTrap from "primevue/focustrap";
import { ShelleyPreset } from "./theme/shelley-preset";
import "../styles.css";
import "primeicons/primeicons.css";
import "@xterm/xterm/css/xterm.css";
import { initializeTheme } from "../services/theme";
import { initializeNotifications } from "../services/notifications";
import { i18nPlugin } from "./composables/i18n";
import App from "./App.vue";
import type { InitData } from "../types";

export interface ShelleyMountOptions extends Partial<InitData> {
  element: HTMLElement;
}

export interface ShelleyMountHandle {
  unmount(): void;
}

declare global {
  interface Window {
    mountShelleyPanel?: typeof mountShelleyPanel;
  }
}

const primeVueOptions = {
  theme: {
    preset: ShelleyPreset,
    options: {
      darkModeSelector: ".dark",
      cssLayer: { name: "primevue", order: "primevue" },
    },
  },
};

export function mountShelleyPanel(options: ShelleyMountOptions): ShelleyMountHandle {
  const { element, ...init } = options;
  window.__SHELLEY_INIT__ = { ...window.__SHELLEY_INIT__, ...init, presslts_embedded: true } as InitData;
  document.documentElement.dataset.pressltsEmbedded = "true";
  element.classList.add("shelley-embedded-root");

  initializeTheme();
  initializeNotifications();
  const app: VueApp = createApp(App);
  app.use(PrimeVue, primeVueOptions);
  app.use(i18nPlugin);
  app.directive("tooltip", Tooltip);
  app.directive("focustrap", FocusTrap);
  app.mount(element);

  return {
    unmount() {
      app.unmount();
      element.classList.remove("shelley-embedded-root");
    },
  };
}

if (typeof window !== "undefined") {
  window.mountShelleyPanel = mountShelleyPanel;
}
