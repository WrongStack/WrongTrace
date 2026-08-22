/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // WrongTrace brand palette: dark-first, single accent (indigo).
        bg: {
          base: '#0b0d12',
          panel: '#11141b',
          raised: '#161a23',
        },
        accent: {
          // Indigo-leaning accent used for primary CTAs, charts, focus.
          DEFAULT: '#6366f1',
          soft: '#818cf8',
        },
        signal: {
          added: '#22c55e',
          modified: '#eab308',
          deleted: '#ef4444',
        },
      },
      fontFamily: {
        sans: ['Inter', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
    },
  },
  plugins: [],
};
