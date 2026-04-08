# Goal is for customers to be ready to ship micro-SaaS

It needs to stay as generic as possible.

Hierarchy

Organization
:has_many Team

Team
:has_many User

User

Customer
:oneof Organization/Team/User


Permissions

RBAC model
