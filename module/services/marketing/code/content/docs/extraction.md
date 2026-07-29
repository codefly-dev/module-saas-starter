---
type: doc
slug: extraction
title: Extract the marketing service
description: Copy the public service into an isolated build context without product business code.
locale: en
state: published
revision: extraction-v1
author: Starter Maintainers
reviewer: Starter Reviewers
publishedAt: 2026-07-28T09:00:00.000Z
updatedAt: 2026-07-28T09:00:00.000Z
tags:
  - extraction
navigationOrder: 30
category: operations
affectedFeatures: []
---
## Dependency graph

The marketing service has no source dependency on another application or shared filesystem at build or runtime.

```text
marketing
  -> generated public site configuration
  -> repository content provider
  -> optional HTTPS public plan projection
  -> configured product handoff origin
```

The generated public configuration is the only brand artifact also consumed by the product frontend. Reverse imports from that generated artifact into either application are impossible because it contains data only.

## Verify extraction

The repository extraction gate copies only `module/services/marketing` into a temporary directory, installs from its lockfile, runs unit and quality checks, and builds with the catalog pointed at an unavailable address.

No accounts source, frontend source, database migration, secret configuration, or workspace-relative package is present in that context.

## Move later

Move the service directory to a separate repository, keep the public configuration generator output, configure the public catalog and product origins, and preserve the service's build command. Business logic does not need rewriting.
