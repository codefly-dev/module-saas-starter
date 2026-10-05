-- Reverses 17_oauth_resource_indicators. Dropping the columns drops every
-- audience binding with them: tokens already minted keep the audience in their
-- signed claims, so a rotation after this ran would reissue them unbound and
-- the gateway would stop refusing one solution's token at another's. A downgrade
-- past this migration therefore wants those sessions revoked, which is what
-- the forward direction's own rotation does within one refresh lifetime.
ALTER TABLE public.client_authorization_codes DROP COLUMN scope;
ALTER TABLE public.client_authorization_codes DROP COLUMN resource;
ALTER TABLE public.sessions DROP COLUMN resource;
