import { readonly, ref } from "vue";

export const SIDE_BY_SIDE_STORAGE_KEY = "shelley-diff-side-by-side";

export function getSideBySidePreference(): boolean {
  try {
    const stored = localStorage.getItem(SIDE_BY_SIDE_STORAGE_KEY);
    if (stored !== null) return stored === "true";
  } catch {
    // Use the viewport-based default when storage is unavailable.
  }
  return window.innerWidth >= 768;
}

const sideBySidePreference = ref(getSideBySidePreference());

export function setSideBySidePreference(value: boolean): void {
  sideBySidePreference.value = value;
  try {
    localStorage.setItem(SIDE_BY_SIDE_STORAGE_KEY, value ? "true" : "false");
  } catch {
    // The in-memory preference still applies when storage is unavailable.
  }
}

export function useSideBySidePreference() {
  return {
    sideBySidePreference: readonly(sideBySidePreference),
    setSideBySidePreference,
  };
}
