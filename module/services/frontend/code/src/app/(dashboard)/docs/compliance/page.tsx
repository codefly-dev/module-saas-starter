"use client";

import { useState } from "react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/ui";
import {
  Shield,
  ShieldCheck,
  Key,
  Users,
  Activity,
  FileText,
  ChevronDown,
  Lock,
  Server,
  AlertTriangle,
} from "lucide-react";

// ---------------------------------------------------------------------------
// Collapsible section — replaces a full accordion dependency
// ---------------------------------------------------------------------------

function Section({
  icon: Icon,
  title,
  description,
  children,
  defaultOpen = false,
}: {
  icon: React.ElementType;
  title: string;
  description: string;
  children: React.ReactNode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);

  return (
    <Card>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="w-full text-left"
      >
        <CardHeader className="flex flex-row items-center gap-4 space-y-0">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
            <Icon className="h-5 w-5 text-primary" />
          </div>
          <div className="flex-1 min-w-0">
            <CardTitle className="text-base">{title}</CardTitle>
            <CardDescription className="mt-1">{description}</CardDescription>
          </div>
          <ChevronDown
            className={`h-5 w-5 shrink-0 text-muted-foreground transition-transform ${
              open ? "rotate-180" : ""
            }`}
          />
        </CardHeader>
      </button>
      {open && (
        <CardContent className="pt-0 pl-[4.5rem]">
          <div className="prose prose-sm dark:prose-invert max-w-none">
            {children}
          </div>
        </CardContent>
      )}
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export default function CompliancePage() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">
          Security &amp; Compliance
        </h1>
        <p className="text-muted-foreground mt-1">
          Overview of security controls, data handling practices, and compliance
          posture. This documentation supports SOC 2 Type II, GDPR, and
          enterprise security review processes.
        </p>
      </div>

      <div className="space-y-4">
        {/* ── Security Overview ─────────────────────────────────── */}
        <Section
          icon={ShieldCheck}
          title="Security Overview"
          description="Authentication, encryption, and rate limiting"
          defaultOpen
        >
          <h4 className="font-semibold mt-0">Authentication</h4>
          <ul>
            <li>
              JWT-based authentication with short-lived access tokens (15 min)
              and rotating refresh tokens.
            </li>
            <li>
              OAuth 2.0 / OpenID Connect via configurable providers (Google,
              WorkOS, Auth0).
            </li>
            <li>
              Passwordless magic link sign-in as an alternative to OAuth.
            </li>
            <li>
              CSRF protection with cryptographic state tokens on all OAuth
              redirects.
            </li>
          </ul>

          <h4 className="font-semibold">Encryption</h4>
          <ul>
            <li>
              TLS 1.2+ enforced on all external connections. Internal
              service-to-service traffic uses mTLS where supported.
            </li>
            <li>
              Secrets (database credentials, API keys, signing keys) stored in
              HashiCorp Vault with dynamic secret rotation.
            </li>
            <li>Database-level encryption at rest (AES-256).</li>
          </ul>

          <h4 className="font-semibold">Rate Limiting</h4>
          <ul>
            <li>
              Per-IP and per-user rate limiting at the gateway layer to
              prevent brute-force and abuse.
            </li>
            <li>
              Adaptive throttling with exponential back-off on repeated
              authentication failures.
            </li>
          </ul>
        </Section>

        {/* ── Data Handling ─────────────────────────────────────── */}
        <Section
          icon={Lock}
          title="Data Handling"
          description="GDPR compliance, data export, deletion, and retention"
        >
          <h4 className="font-semibold mt-0">GDPR Data Subject Rights</h4>
          <ul>
            <li>
              <strong>Right to Access / Export:</strong> Users can request a
              full export of their personal data via the Settings page. Export
              is delivered as a structured JSON archive.
            </li>
            <li>
              <strong>Right to Erasure:</strong> Account deletion permanently
              removes all personal data within 30 days. Anonymized aggregate
              analytics may be retained.
            </li>
            <li>
              <strong>Right to Rectification:</strong> Users can update their
              profile information at any time through the application UI.
            </li>
          </ul>

          <h4 className="font-semibold">Data Retention</h4>
          <ul>
            <li>Active account data is retained for the lifetime of the account.</li>
            <li>
              Audit logs are retained for a minimum of 1 year (configurable
              per tenant).
            </li>
            <li>
              Session tokens and refresh tokens are automatically purged
              upon expiry or logout.
            </li>
            <li>
              Backups are retained for 90 days with point-in-time recovery
              capability.
            </li>
          </ul>

          <h4 className="font-semibold">Data Processing Agreement</h4>
          <p>
            A Data Processing Agreement (DPA) is available upon request for
            enterprise customers. The DPA covers sub-processor disclosures,
            cross-border transfer mechanisms (Standard Contractual Clauses),
            and breach notification commitments (72-hour window).
          </p>
        </Section>

        {/* ── Access Control ───────────────────────────────────── */}
        <Section
          icon={Users}
          title="Access Control"
          description="Role-based access, audit logging, and multi-factor authentication"
        >
          <h4 className="font-semibold mt-0">Role-Based Access Control (RBAC)</h4>
          <ul>
            <li>
              Three-tier role model: <strong>Owner</strong>,{" "}
              <strong>Admin</strong>, <strong>Member</strong> at the
              organization level.
            </li>
            <li>
              Platform-level roles (<code>super_admin</code>,{" "}
              <code>billing</code>, <code>support</code>) for cross-tenant
              operations.
            </li>
            <li>
              Team-based scoping allows fine-grained access within
              organizations.
            </li>
            <li>
              All role assignments are logged in the audit trail.
            </li>
          </ul>

          <h4 className="font-semibold">Audit Logging</h4>
          <ul>
            <li>
              Append-only audit log captures all security-relevant events:
              authentication, role changes, data access, and configuration
              modifications.
            </li>
            <li>
              Audit entries include actor identity, IP address, timestamp,
              and affected resource.
            </li>
            <li>
              Queryable via the Audit Log page with filtering by action
              type, actor, and date range.
            </li>
          </ul>

          <h4 className="font-semibold">Multi-Factor Authentication</h4>
          <ul>
            <li>
              MFA enforcement is available per-organization. Admins can
              require MFA for all members.
            </li>
            <li>
              Supported factors: TOTP (authenticator apps), WebAuthn/FIDO2
              (hardware keys), and email-based verification.
            </li>
          </ul>
        </Section>

        {/* ── Infrastructure ───────────────────────────────────── */}
        <Section
          icon={Server}
          title="Infrastructure"
          description="Gateway architecture, route whitelisting, and service isolation"
        >
          <h4 className="font-semibold mt-0">Gateway Architecture</h4>
          <ul>
            <li>
              All client traffic routes through an Envoy-based API gateway
              that enforces authentication, rate limiting, and request
              validation before reaching backend services.
            </li>
            <li>
              Explicit route whitelisting: only declared REST endpoints are
              exposed. Internal gRPC services are never directly accessible.
            </li>
            <li>
              OpenAPI specification is generated from the merged gateway
              endpoint, ensuring documentation always matches the live API.
            </li>
          </ul>

          <h4 className="font-semibold">Service Isolation</h4>
          <ul>
            <li>
              Each backend service runs in its own isolated process with
              independent resource limits.
            </li>
            <li>
              Service-to-service communication uses gRPC with mTLS and
              explicit dependency declarations.
            </li>
            <li>
              Database-per-service pattern: each service owns its data store
              with no cross-service direct database access.
            </li>
          </ul>

          <h4 className="font-semibold">Infrastructure as Code</h4>
          <ul>
            <li>
              All infrastructure is managed declaratively through codefly
              service definitions. No manual provisioning.
            </li>
            <li>
              Container images are built deterministically with pinned
              dependency versions.
            </li>
          </ul>
        </Section>

        {/* ── Incident Response ────────────────────────────────── */}
        <Section
          icon={AlertTriangle}
          title="Incident Response"
          description="Response process, escalation, and communication"
        >
          <h4 className="font-semibold mt-0">Incident Classification</h4>
          <ul>
            <li>
              <strong>P1 (Critical):</strong> Data breach, complete service
              outage, or unauthorized access to production systems. Target
              response: 15 minutes.
            </li>
            <li>
              <strong>P2 (High):</strong> Partial service degradation,
              authentication failures, or suspected security incident.
              Target response: 1 hour.
            </li>
            <li>
              <strong>P3 (Medium):</strong> Non-critical bugs, performance
              degradation, or monitoring alerts. Target response: 4 hours.
            </li>
          </ul>

          <h4 className="font-semibold">Response Process</h4>
          <ol>
            <li>
              <strong>Detection:</strong> Automated monitoring, audit log
              anomaly detection, or customer report.
            </li>
            <li>
              <strong>Triage:</strong> On-call engineer classifies severity
              and assembles response team.
            </li>
            <li>
              <strong>Containment:</strong> Isolate affected systems, revoke
              compromised credentials, enable enhanced logging.
            </li>
            <li>
              <strong>Remediation:</strong> Apply fix, verify resolution,
              restore normal operations.
            </li>
            <li>
              <strong>Post-mortem:</strong> Root cause analysis, timeline
              reconstruction, and preventive measures documented within 5
              business days.
            </li>
          </ol>

          <h4 className="font-semibold">Customer Notification</h4>
          <p>
            Affected customers are notified within 72 hours of a confirmed
            data breach, in compliance with GDPR Article 33. Status updates
            are provided via email and an incident status page.
          </p>
        </Section>

        {/* ── Compliance Programs ──────────────────────────────── */}
        <Section
          icon={FileText}
          title="Compliance Programs"
          description="SOC 2, GDPR, and ongoing compliance commitments"
        >
          <h4 className="font-semibold mt-0">SOC 2 Type II</h4>
          <p>
            This platform is designed to meet SOC 2 Trust Services Criteria
            for Security, Availability, and Confidentiality. Key controls
            include:
          </p>
          <ul>
            <li>Centralized authentication with JWT and OAuth 2.0.</li>
            <li>Role-based access control with audit trail.</li>
            <li>Encryption in transit (TLS 1.2+) and at rest (AES-256).</li>
            <li>Automated vulnerability scanning and dependency updates.</li>
            <li>Incident response procedures with documented SLAs.</li>
          </ul>

          <h4 className="font-semibold">GDPR</h4>
          <ul>
            <li>Data minimization: only necessary personal data is collected.</li>
            <li>
              Lawful basis for processing documented per data category.
            </li>
            <li>
              Sub-processor registry maintained and available on request.
            </li>
            <li>
              Data Protection Impact Assessments (DPIA) conducted for
              high-risk processing activities.
            </li>
          </ul>

          <h4 className="font-semibold">Penetration Testing</h4>
          <p>
            Annual third-party penetration testing is conducted. Summary
            findings and remediation status are available to enterprise
            customers under NDA.
          </p>
        </Section>
      </div>
    </div>
  );
}
