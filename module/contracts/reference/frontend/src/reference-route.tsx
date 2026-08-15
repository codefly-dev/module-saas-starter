import { presentReferenceSummary } from "./controller.js";

export default function ReferenceRoute() {
	const summary = presentReferenceSummary(true);
	return (
		<main>
			<h1>{summary.title}</h1>
		</main>
	);
}
