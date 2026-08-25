# Scenarios — end-to-end narratives

> **Strawman for review.** These cut across stories to show the behaviors
> working together. They double as integration-test outlines. Rule IDs from
> [`behaviors.md`](behaviors.md) in brackets.

## Scenario 1 — Onboarding a scoped analyst

1. Owner sets up the scope tree: `foundation → solution_x, solution_y`, each with
   customers beneath [B4-ready].
2. Admin grants an analyst `viewer @ foundation.solution_x` [H1].
3. Analyst logs in, sees every record under `solution_x` [B4], and no hint that
   `solution_y` exists [B6, B2].
4. Admin adds `editor @ …solution_x.customer_7`; the analyst can now edit under
   `customer_7`, still read-only elsewhere [B5, H2].
5. Admin revokes the `solution_x` viewer grant; all of the analyst's access under
   `solution_x` disappears in one action, `customer_7` editor remains.

## Scenario 2 — Collaborating with an external auditor

1. A member owns quarter-close records under `…customer_7`.
2. Admin shares the `customer_7` subtree with an external auditor, role viewer,
   expiry +30 days [S3, B9].
3. The auditor signs in, sees only the shared subtree [B1, B6], with internal
   annotation fields blanked [F2, B15].
4. Day 31: the auditor's access is gone automatically, no manual revoke [B9].
5. The member lists shares on a record and confirms none remain [S4].

## Scenario 3 — An agent deploys on Sarah's behalf, audited

1. Sarah (Owner) authorizes a deploy-agent to act for her, scoped to
   `foundation.solution_x`, for one task [A1, B11].
2. The agent spawns a child worker; the child can do at most the agent's subset
   [A2, B11]; an attempt to touch `solution_y` is rejected at issue and at use
   [B2].
3. A sensitive step needs elevation; the agent requests it with a justification,
   Sarah approves once; a single-use authority is minted recording her approval
   [A3, B12].
4. Weeks later, an auditor reconstructs: "child worker of deploy-agent, acting for
   Sarah under approval #Z, performed the deploy" — from a durable record [A4,
   B14]. *(This last step is the durability gap RFC-0003 closes.)*

## What these scenarios pin down
- The **visibility filter runs first** (metadata before content) in every read.
- **Most-specific-wins and highest-grant-wins** coexist without the user picking
  a mode.
- **Attenuation** holds across agent → child → elevation.
- **Audit** must join three primitives (approval, capability hop, action) that
  are disconnected today.
