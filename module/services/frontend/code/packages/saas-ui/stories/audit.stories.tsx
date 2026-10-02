import { LastLogin } from "../src/index.js";

export default { title: "SaaS UI/Audit" };

// A fixed clock, so a story renders the same words on every run.
const now = () => new Date("2026-10-02T10:00:12Z");

export const LastLoginSecondsAgo = {
	render: () => <LastLogin at="2026-10-02T10:00:00Z" subject="ana@example.com" now={now} />,
};

export const LastLoginYesterday = {
	render: () => <LastLogin at="2026-10-01T08:30:00Z" now={now} />,
};

export const LastLoginNeverRecorded = {
	render: () => <LastLogin at={null} now={now} />,
};
