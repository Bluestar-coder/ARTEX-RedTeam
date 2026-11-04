import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

export const APP_CONFIG = {
  name: "ARTEX-RedTeam",
  version: packageJson.version,
  copyright: `© ${currentYear}, ARTEX-RedTeam.`,
  meta: {
    title: "ARTEX-RedTeam — 红队作战控制台",
    description: "LLM 驱动的自主红队作战系统控制台",
  },
};
