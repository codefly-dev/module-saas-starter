-- Migration 76: canonical subscription-management email templates.
--
-- Keep external billing messages limited to variables the durable billing
-- producer owns. This is a new migration rather than an edit to migration 75
-- so databases that already recorded the initial email convergence are also
-- upgraded. Fresh installs already receive the same copy from migration 15.

UPDATE email_templates AS template
SET subject_template = canonical.subject_template,
    html_template = canonical.html_template,
    text_template = canonical.text_template,
    version = template.version + 1,
    updated_at = CURRENT_TIMESTAMP
FROM (VALUES
    (
        'payment_failed',
        'Subscription payment failed',
        '<h1>Payment Failed</h1><p>We couldn''t process your subscription payment. <a href="{{billing_url}}">Manage billing</a> to update your payment method.</p>',
        'We couldn''t process your subscription payment. Manage billing: {{billing_url}}'
    ),
    (
        'invoice_ready',
        'Subscription payment received',
        '<h1>Payment Received</h1><p>Your subscription payment was received. <a href="{{billing_url}}">View billing</a>.</p>',
        'Your subscription payment was received. View billing: {{billing_url}}'
    ),
    (
        'trial_ending',
        'Your subscription trial is ending soon',
        '<h1>Trial Ending Soon</h1><p>Your subscription trial is ending soon. <a href="{{billing_url}}">Manage your subscription</a> to keep your account.</p>',
        'Your subscription trial is ending soon. Manage your subscription: {{billing_url}}'
    )
) AS canonical(name, subject_template, html_template, text_template)
WHERE template.name = canonical.name
  AND (
      template.subject_template IS DISTINCT FROM canonical.subject_template
      OR template.html_template IS DISTINCT FROM canonical.html_template
      OR template.text_template IS DISTINCT FROM canonical.text_template
  );
