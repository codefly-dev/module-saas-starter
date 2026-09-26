import { NoReadableCollection } from "../src/index.js";

export default { title: "SaaS UI/No readable collection" };

/** A member: whom to ask, and where the grant is made. */
export const Member = {
	render: () => <NoReadableCollection canGrant={false} />,
};
/** An organization administrator: the link to make the grant. */
export const Administrator = {
	render: () => <NoReadableCollection canGrant />,
};
/** A chat that has nothing to answer from. */
export const Chat = {
	render: () => (
		<NoReadableCollection canGrant={false} subject="documents to answer from" />
	),
};
