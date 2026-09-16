// Shelley PrimeVue theme preset. Extends Aura but re-points the primary
// palette to the app's blue (Aura defaults to emerald) and tunes the Button
// component tokens so PrimeVue <Button> renders like the legacy .btn-* classes
// it replaces (0.25rem radius, 0.875rem label, 500 weight, compact padding).
//
// Colors are wired to our existing CSS variables (styles.css :root/.dark) so
// light/dark mode keeps working through the same source of truth. Because the
// preset is global, migrated Buttons need no per-instance :dt for the common
// cases; only genuinely bespoke buttons keep local overrides.
import { definePreset } from "@primeuix/themes";
import Aura from "@primeuix/themes/aura";

export const ShelleyPreset = definePreset(Aura, {
  semantic: {
    // Re-point the primary ramp to blue. Values mirror Tailwind blue, which
    // is what --primary (#2563eb) / --primary-dark (#1d4ed8) already use.
    primary: {
      50: "{blue.50}",
      100: "{blue.100}",
      200: "{blue.200}",
      300: "{blue.300}",
      400: "{blue.400}",
      500: "{blue.500}",
      600: "{blue.600}",
      700: "{blue.700}",
      800: "{blue.800}",
      900: "{blue.900}",
      950: "{blue.950}",
    },
    colorScheme: {
      light: {
        primary: {
          color: "var(--primary)",
          contrastColor: "#ffffff",
          hoverColor: "var(--primary-dark)",
          activeColor: "var(--primary-dark)",
        },
      },
      dark: {
        primary: {
          color: "var(--primary)",
          contrastColor: "#ffffff",
          hoverColor: "var(--primary-dark)",
          activeColor: "var(--primary-dark)",
        },
      },
    },
  },
  components: {
    button: {
      root: {
        // Match the legacy .btn sizing: 0.25rem radius, 14px label, medium
        // weight, and the same paddings the .btn-primary/.btn-secondary used.
        borderRadius: "0.25rem",
        paddingX: "1rem",
        paddingY: "0.5rem",
        label: { fontWeight: "500" },
        sm: { fontSize: "0.875rem", paddingX: "0.75rem", paddingY: "0.375rem" },
        iconOnlyWidth: "2.25rem",
        gap: "0.375rem",
      },
      colorScheme: {
        light: {
          root: {
            // .btn-secondary was a light surface with a real border and
            // primary text — reproduce that rather than Aura's grey fill.
            secondary: {
              background: "var(--bg-secondary)",
              hoverBackground: "var(--bg-tertiary)",
              activeBackground: "var(--bg-tertiary)",
              borderColor: "var(--border)",
              hoverBorderColor: "var(--border)",
              activeBorderColor: "var(--border)",
              color: "var(--text-primary)",
              hoverColor: "var(--text-primary)",
              activeColor: "var(--text-primary)",
            },
          },
        },
        dark: {
          root: {
            secondary: {
              background: "var(--bg-secondary)",
              hoverBackground: "var(--bg-tertiary)",
              activeBackground: "var(--bg-tertiary)",
              borderColor: "var(--border)",
              hoverBorderColor: "var(--border)",
              activeBorderColor: "var(--border)",
              color: "var(--text-primary)",
              hoverColor: "var(--text-primary)",
              activeColor: "var(--text-primary)",
            },
          },
        },
      },
    },
  },
});
