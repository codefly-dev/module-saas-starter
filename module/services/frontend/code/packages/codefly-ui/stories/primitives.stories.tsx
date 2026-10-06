import { useState } from "react";
import {
	Banner,
	Button,
	Input,
	Textarea,
	Label,
	Checkbox,
	Switch,
	Select,
	SelectTrigger,
	SelectValue,
	SelectContent,
	SelectItem,
	Badge,
	Avatar,
	AvatarImage,
	AvatarFallback,
	Skeleton,
	Separator,
	Dialog,
	DialogTrigger,
	DialogContent,
	DialogHeader,
	DialogTitle,
	DialogDescription,
	DialogFooter,
	DialogClose,
	AlertDialog,
	AlertDialogTrigger,
	AlertDialogContent,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogCancel,
	AlertDialogAction,
	Tooltip,
	TooltipTrigger,
	TooltipContent,
	Popover,
	PopoverTrigger,
	PopoverContent,
	PopoverTitle,
	PopoverDescription,
	DropdownMenu,
	DropdownMenuTrigger,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	EmptyState,
	ErrorState,
	Notice,
	Card,
	Chip,
	ChipGroup,
	List,
	ListItem,
	DescriptionList,
} from "../src/layout/index.js";
import { FileTextIcon, TriangleAlertIcon, UserIcon } from "lucide-react";

export default { title: "Shared UI/Primitives" };
export const Buttons = {
	render: () => (
		<div>
			<Button>Save</Button>
			<Button variant="outline">Cancel</Button>
			<Button variant="destructive">Delete</Button>
			<Button disabled>Unavailable</Button>
			<Button disabled aria-busy>
				Saving…
			</Button>
		</div>
	),
};
export const Fields = {
	render: () => (
		<div>
			<Label htmlFor="name">Name</Label>
			<Input id="name" placeholder="Jane Doe" />
			<Label htmlFor="notes">Notes</Label>
			<Textarea id="notes" />
			<Input aria-label="Read only" readOnly value="Acme" />
			<Input aria-label="Disabled" disabled />
			<Input
				aria-label="Invalid email"
				aria-invalid
				aria-describedby="email-error"
				defaultValue="invalid"
			/>
			<p id="email-error">Enter a valid email address.</p>
		</div>
	),
};
export const CheckboxesAndSwitches = {
	render: () => (
		<div>
			<label>
				<Checkbox defaultChecked />
				Notifications
			</label>
			<label>
				<Checkbox disabled />
				Unavailable
			</label>
			<label>
				<Switch defaultChecked />
				Email updates
			</label>
			<label>
				<Switch disabled />
				Unavailable updates
			</label>
		</div>
	),
};
export const Selection = {
	render: () => (
		<Select defaultValue="one">
			<SelectTrigger aria-label="Workspace">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				<SelectItem value="one">Acme</SelectItem>
				<SelectItem value="two">ExampleCorp</SelectItem>
				<SelectItem value="three" disabled>
					Unavailable
				</SelectItem>
			</SelectContent>
		</Select>
	),
};
export const BadgesAndAvatars = {
	render: () => (
		<div>
			<Badge>Active</Badge>
			<Badge variant="outline">Pending</Badge>
			<Badge variant="destructive">Failed</Badge>
			<Avatar>
				<AvatarImage
					src="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg'/%3E"
					alt="Jane Doe"
				/>
				<AvatarFallback>JD</AvatarFallback>
			</Avatar>
		</div>
	),
};
export const Loading = {
	render: () => (
		<div aria-label="Loading content" aria-busy>
			<Skeleton className="h-6 w-48" />
			<Separator />
			<Skeleton className="h-24 w-full" />
		</div>
	),
};
export const Modal = {
	render: () => (
		<Dialog>
			<DialogTrigger render={<Button />}>Edit workspace</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Edit workspace</DialogTitle>
					<DialogDescription>Update the workspace name.</DialogDescription>
				</DialogHeader>
				<Label htmlFor="workspace-name">Name</Label>
				<Input id="workspace-name" defaultValue="Acme" />
				<DialogFooter>
					<DialogClose render={<Button variant="outline" />}>
						Cancel
					</DialogClose>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	),
};
export const Confirmation = {
	render: () => (
		<AlertDialog>
			<AlertDialogTrigger render={<Button variant="destructive" />}>
				Delete example
			</AlertDialogTrigger>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete example?</AlertDialogTitle>
					<AlertDialogDescription>
						This preview has no destructive operation attached.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel>Cancel</AlertDialogCancel>
					<AlertDialogAction>Confirm</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	),
};
export const TooltipPreview = {
	render: () => (
		<Tooltip>
			<TooltipTrigger render={<Button />}>Help</TooltipTrigger>
			<TooltipContent>Workspace help</TooltipContent>
		</Tooltip>
	),
};
export const PopoverPreview = {
	render: () => (
		<Popover>
			<PopoverTrigger render={<Button />}>About this number</PopoverTrigger>
			<PopoverContent>
				<PopoverTitle>Weekly requests</PopoverTitle>
				<PopoverDescription>Requests received, per week.</PopoverDescription>
			</PopoverContent>
		</Popover>
	),
};
export const Menu = {
	render: () => (
		<DropdownMenu>
			<DropdownMenuTrigger render={<Button />}>Actions</DropdownMenuTrigger>
			<DropdownMenuContent>
				<DropdownMenuItem>View details</DropdownMenuItem>
				<DropdownMenuSeparator />
				<DropdownMenuItem disabled>Unavailable action</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	),
};
// An item can say something about itself after its label, muted: a state worth
// knowing before choosing it. The label alone stays its accessible name.
export const MenuHints = {
	render: () => (
		<DropdownMenu>
			<DropdownMenuTrigger render={<Button />}>
				Add a report
			</DropdownMenuTrigger>
			<DropdownMenuContent>
				<DropdownMenuItem>Weekly requests</DropdownMenuItem>
				<DropdownMenuItem hint="Empty">Monthly invoices</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	),
};
export const Empty = {
	render: () => (
		<EmptyState
			heading="No resources"
			description="Create a resource to get started."
		>
			<Button>Create resource</Button>
		</EmptyState>
	),
};
export const Error = {
	render: () => (
		<ErrorState
			title="Unable to load resources."
			detail="The data provider is unavailable."
		/>
	),
};

export const NotificationBanner = {
	render: function BannerExample() {
		const [visible, setVisible] = useState(true);
		return visible ? (
			<Banner
				title="Source updated"
				onDismiss={() => setVisible(false)}
				actions={<Button variant="link">View</Button>}
			>
				2 changes are ready.
			</Banner>
		) : (
			<Button onClick={() => setVisible(true)}>Show notification</Button>
		);
	},
};

// A decision the user may not be able to make still has a way out: the escape
// is a required prop the kit always renders.
export const DecisionNotice = {
	render: function NoticeExample() {
		const [visible, setVisible] = useState(true);
		return visible ? (
			<Notice
				placement="inline"
				title="Review the updated terms"
				escape={{ label: "Sign out", onSelect: () => setVisible(false) }}
				actions={<Button disabled>Accept terms</Button>}
			>
				Accepting is unavailable until the terms are published.
			</Notice>
		) : (
			<Button onClick={() => setVisible(true)}>Show notice</Button>
		);
	},
};

// The three states a declared source can be in, told apart by tone and by
// the dot's shape as well as by their words.
export const StatusBadges = {
	render: () => (
		<div>
			<Badge tone="neutral" dot>
				Set up
			</Badge>
			<Badge tone="success" dot>
				Connected
			</Badge>
			<Badge tone="danger" dot>
				Error
			</Badge>
			<Badge tone="warning">Paused</Badge>
			<Badge tone="info">Syncing</Badge>
			<Badge tone="success" size="sm" dot>
				Live
			</Badge>
			<Badge tone="info" size="lg" dot>
				In review
			</Badge>
		</div>
	),
};

export const Chips = {
	render: function ChipsExample() {
		const [owners, setOwners] = useState(["Jane Doe", "Acme Finance"]);
		return (
			<div>
				<ChipGroup label="Affects">
					<Chip href="#pricing" icon={<FileTextIcon />} meta="unread">
						Pricing change
					</Chip>
					<Chip href="#onboarding" icon={<FileTextIcon />}>
						Onboarding flow
					</Chip>
				</ChipGroup>
				<ChipGroup label="Owners">
					{owners.map((owner) => (
						<Chip
							key={owner}
							icon={<UserIcon />}
							onRemove={() =>
								setOwners((current) => current.filter((o) => o !== owner))
							}
							removeLabel={`Remove ${owner}`}
						>
							{owner}
						</Chip>
					))}
				</ChipGroup>
				<ChipGroup label="Status">
					<Chip tone="success" onClick={() => {}}>
						Accepted
					</Chip>
					<Chip tone="warning">Needs review</Chip>
					<Chip tone="danger">Rejected</Chip>
					<Chip tone="info" meta="3">
						Comments
					</Chip>
				</ChipGroup>
			</div>
		);
	},
};

export const StatusBanners = {
	render: () => (
		<div>
			<Banner tone="info" title="Local preview">
				Nothing on this page reached a host.
			</Banner>
			<Banner tone="success" title="Source connected" />
			<Banner tone="warning" title="Sync paused">
				Scheduled pulls resume once the credential is renewed.
			</Banner>
			<Banner
				tone="danger"
				title="Could not reach the source"
				actions={<Button variant="outline">Retry</Button>}
			/>
		</div>
	),
};

export const Lists = {
	render: () => (
		<div>
			<List label="Parsing notes" variant="divided">
				<ListItem
					icon={<TriangleAlertIcon />}
					description="Line 12: front matter has no owner."
					meta="proposal.md"
				>
					Missing owner
				</ListItem>
				<ListItem
					icon={<TriangleAlertIcon />}
					description="Two headings share level two."
					actions={<Button variant="ghost">Open</Button>}
				>
					Heading level reused
				</ListItem>
			</List>
			<List label="Related">
				<ListItem meta="2 days ago">Pricing change</ListItem>
				<ListItem meta="last week">Onboarding flow</ListItem>
			</List>
		</div>
	),
};

export const DescriptionLists = {
	render: () => (
		<div>
			<DescriptionList
				items={[
					{ term: "Status", value: <Badge tone="info">In review</Badge> },
					{ term: "Owner", value: "Jane Doe" },
					{ term: "Updated", value: "2 October 2026" },
				]}
			/>
			<DescriptionList
				layout="stacked"
				items={[
					{
						term: "Summary",
						value:
							"Move the starter plan to annual billing and keep monthly for existing customers.",
					},
				]}
			/>
		</div>
	),
};

export const CardWithDescription = {
	render: () => (
		<Card
			title="Declared source"
			description="Declared by this solution, not chosen here."
			actions={<Button variant="outline">Sync now</Button>}
		>
			<Badge tone="success" dot>
				Connected
			</Badge>
		</Card>
	),
};

export const EmptyWithReference = {
	render: () => (
		<EmptyState
			heading="No proposals to read"
			description="This source has no readable proposals yet."
			reference={
				<>
					The <a href="#source">source card above</a> says where this stands.
				</>
			}
		/>
	),
};
