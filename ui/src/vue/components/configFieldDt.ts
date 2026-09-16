// Design tokens for the PrimeVue form controls rendered by ConfigFieldInput.vue
// (Select and InputText; password fields are just <InputText type="password">,
// which shares the inputtext tokens). Rather than override PrimeVue's internal
// .p-* classes, we feed our theme's CSS variables straight into PrimeVue's own
// token system via the :dt prop so it renders itself using our colors. These
// values reproduce the legacy .form-input look (var(--bg-base) fill, subtle
// border, --primary focus ring) that the bare <input>/<select> used before.
const focusRing = {
  width: "2px",
  style: "none",
  color: "transparent",
  offset: "0",
  shadow: "0 0 0 2px rgba(37, 99, 235, 0.2)",
};

// InputText (text and password fields both use PrimeVue's inputtext tokens).
export const inputFieldDt = {
  background: "var(--bg-base)",
  color: "var(--text-primary)",
  borderColor: "var(--border)",
  hoverBorderColor: "var(--border)",
  focusBorderColor: "var(--primary)",
  borderRadius: "0.375rem",
  shadow: "none",
  paddingX: "0.75rem",
  paddingY: "0.5rem",
  placeholderColor: "var(--text-secondary)",
  focusRing,
};

// Select trigger + overlay.
export const selectFieldDt = {
  background: "var(--bg-base)",
  color: "var(--text-primary)",
  borderColor: "var(--border)",
  hoverBorderColor: "var(--border)",
  focusBorderColor: "var(--primary)",
  borderRadius: "0.375rem",
  shadow: "none",
  paddingX: "0.75rem",
  paddingY: "0.5rem",
  placeholderColor: "var(--text-secondary)",
  focusRing,
  dropdown: { color: "var(--text-secondary)" },
  overlay: {
    background: "var(--bg-secondary)",
    borderColor: "var(--border)",
    borderRadius: "0.375rem",
    color: "var(--text-primary)",
    shadow: "0 4px 12px rgba(0, 0, 0, 0.3)",
  },
  option: {
    color: "var(--text-primary)",
    padding: "0.5rem 0.75rem",
    borderRadius: "0",
    focusBackground: "var(--bg-tertiary)",
    focusColor: "var(--text-primary)",
    selectedBackground: "var(--bg-tertiary)",
    selectedColor: "var(--text-primary)",
    selectedFocusBackground: "var(--bg-tertiary)",
    selectedFocusColor: "var(--text-primary)",
  },
};
