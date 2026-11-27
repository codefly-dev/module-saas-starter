# Render Accounts' host-specific seed from the local authority's canonical cast.
# ruby scripts/render-authority-fixture.rb path/to/authority [--check]
require "erb"
require "yaml"
require "pathname"

abort "usage: ruby scripts/render-authority-fixture.rb AUTHORITY_CHECKOUT [--check]" unless (1..2).cover?(ARGV.size) && [nil, "--check"].include?(ARGV[1])
source = Pathname(ARGV.fetch(0)) / "modules/authority/services/local/code/fixtures/local.yaml"
fixture = YAML.safe_load_file(source)
people = fixture.fetch("people").to_h { |p| [p.fetch("id"), p] }
acme = people.values.select { |p| p.fetch("organization") == "acme" }
owners = acme.select { |p| p.fetch("roles").include?("platform_admin") }
abort "expected exactly one Acme platform owner" unless owners.size == 1
platform_owner = owners.first
installation = fixture.fetch("installations").find { |i| i.fetch("organization") == "acme" && i.fetch("target") == "*" }
# Accounts keeps stable database primary keys across reseeds.
stable_ids = %w[sarah alice bob carol dana].each_with_index.to_h do |id, index|
  [id, "00000000-0000-7000-8000-0000000000a#{index + 1}"]
end

def account_role(person)
  roles = person.fetch("roles")
  return "super_admin" if roles.include?("platform_admin")
  roles.include?("admin") ? "admin" : "member"
end

root = Pathname(__dir__).parent
template = (root / "scripts/fixtures/dev-admin.yaml.erb").read
output = ERB.new(template, trim_mode: "-").result(binding)
%w[module/fixtures/dev-admin.yaml module/services/accounts/code/fixtures/embedded/dev-admin.yaml].each do |file|
  path = root / file
  if ARGV[1] == "--check"
    abort "#{file} is stale" unless path.read == output
  else
    path.write(output)
  end
end
