/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        canvas: "#090c10",
        panel: "#101720",
        panel2: "#151d28",
        line: "rgba(171, 193, 220, 0.16)",
        muted: "#8d9aab",
        text: "#eef4ff",
        burst: "#7bf3b1",
        session: "#78b9ff",
        process: "#ffcb72",
        warn: "#ff9578",
      },
      fontFamily: {
        sans: ["Inter", "SF Pro Text", "system-ui", "sans-serif"],
      },
      boxShadow: {
        panel: "0 18px 36px rgba(0, 0, 0, 0.28)",
      },
    },
  },
  plugins: [],
};
