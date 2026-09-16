// Design tokens for the Manage Models PrimeVue DataTable (ModelsModal.vue).
// Feeds our theme's CSS variables into PrimeVue's own token system via the :dt
// prop (same approach as statusPickerDt) so the table renders in our colors
// with a compact, data-dense layout instead of PrimeVue's roomy defaults.
// Paired with size="small", which selects the sm.* padding below.
export const modelsTableDt = {
  headerCell: {
    background: "var(--bg-secondary)",
    color: "var(--text-tertiary)",
    borderColor: "var(--border)",
    sm: { padding: "0.4rem 0.6rem" },
  },
  columnTitle: { fontWeight: "500" },
  bodyCell: {
    borderColor: "var(--border)",
    sm: { padding: "0.35rem 0.6rem" },
  },
  row: {
    background: "var(--bg-base)",
    color: "var(--text-primary)",
    hoverBackground: "var(--bg-secondary)",
    hoverColor: "var(--text-primary)",
  },
};
