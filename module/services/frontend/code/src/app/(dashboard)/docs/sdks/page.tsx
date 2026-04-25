"use client";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/shared/ui";

const sdks = [
  {
    id: "javascript",
    label: "JavaScript / TypeScript",
    install: `npm install @connectrpc/connect @connectrpc/connect-web @bufbuild/protobuf`,
    usage: `import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { UserService, AuthService, OrganizationService } from "./gen/saas-starter_api_grpc_pb";

const transport = createConnectTransport({
  baseUrl: "https://api.yourapp.com",
});

// Authenticate
const auth = createClient(AuthService, transport);
const { accessToken } = await auth.loginWithPassword({
  email: "user@example.com",
  password: "secret",
});

// Use authenticated transport
const authedTransport = createConnectTransport({
  baseUrl: "https://api.yourapp.com",
  interceptors: [(next) => async (req) => {
    req.header.set("Authorization", \`Bearer \${accessToken}\`);
    return next(req);
  }],
});

// List users
const users = createClient(UserService, authedTransport);
const { users: userList } = await users.listUsers({ pageSize: 20 });
console.log(userList);

// Create organization
const orgs = createClient(OrganizationService, authedTransport);
const { organization } = await orgs.createOrganization({
  name: "Acme Inc",
  slug: "acme-inc",
});`,
  },
  {
    id: "python",
    label: "Python",
    install: `pip install grpcio grpcio-tools`,
    usage: `import grpc
from gen import api_pb2, api_pb2_grpc

# Connect to the API gateway
channel = grpc.insecure_channel("api.yourapp.com:443")

# Authenticate
auth = api_pb2_grpc.AuthServiceStub(channel)
resp = auth.LoginWithPassword(api_pb2.LoginWithPasswordRequest(
    email="user@example.com",
    password="secret",
))

# Create authenticated metadata
metadata = [("authorization", f"Bearer {resp.access_token}")]

# List users
users_svc = api_pb2_grpc.UserServiceStub(channel)
users = users_svc.ListUsers(
    api_pb2.ListUsersRequest(page_size=20),
    metadata=metadata,
)
for user in users.users:
    print(user.primary_email)

# Create organization
orgs_svc = api_pb2_grpc.OrganizationServiceStub(channel)
org = orgs_svc.CreateOrganization(
    api_pb2.CreateOrganizationRequest(
        name="Acme Inc",
        slug="acme-inc",
    ),
    metadata=metadata,
)`,
  },
  {
    id: "go",
    label: "Go",
    install: `go install connectrpc.com/connect@latest
go install buf.build/gen/go/yourorg/api/connectrpc/go@latest`,
    usage: `package main

import (
    "context"
    "fmt"
    "net/http"

    "connectrpc.com/connect"
    userv1 "gen/user/v1"
    "gen/user/v1/userv1connect"
)

func main() {
    // Create client
    authClient := userv1connect.NewAuthServiceClient(
        http.DefaultClient,
        "https://api.yourapp.com",
    )

    // Authenticate
    resp, err := authClient.LoginWithPassword(
        context.Background(),
        connect.NewRequest(&userv1.LoginWithPasswordRequest{
            Email:    "user@example.com",
            Password: "secret",
        }),
    )
    if err != nil {
        panic(err)
    }
    token := resp.Msg.AccessToken

    // Create authenticated interceptor
    authedClient := userv1connect.NewUserServiceClient(
        http.DefaultClient,
        "https://api.yourapp.com",
        connect.WithInterceptors(
            newAuthInterceptor(token),
        ),
    )

    // List users
    users, err := authedClient.ListUsers(
        context.Background(),
        connect.NewRequest(&userv1.ListUsersRequest{PageSize: 20}),
    )
    if err != nil {
        panic(err)
    }
    for _, u := range users.Msg.Users {
        fmt.Println(u.PrimaryEmail)
    }
}

func newAuthInterceptor(token string) connect.UnaryInterceptorFunc {
    return func(next connect.UnaryFunc) connect.UnaryFunc {
        return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
            req.Header().Set("Authorization", "Bearer "+token)
            return next(ctx, req)
        }
    }
}`,
  },
];

function CodeBlock({ code }: { code: string }) {
  return (
    <pre className="overflow-x-auto rounded-md bg-muted p-4 text-sm">
      <code>{code}</code>
    </pre>
  );
}

export default function SDKDocsPage() {
  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">SDK Documentation</h1>
        <p className="text-muted-foreground">
          Integrate with the API using the Connect protocol. Available for
          JavaScript, Python, and Go.
        </p>
      </div>

      <Tabs defaultValue="javascript" className="w-full">
        <TabsList>
          {sdks.map((sdk) => (
            <TabsTrigger key={sdk.id} value={sdk.id}>
              {sdk.label}
            </TabsTrigger>
          ))}
        </TabsList>

        {sdks.map((sdk) => (
          <TabsContent key={sdk.id} value={sdk.id} className="space-y-4 mt-4">
            <Card>
              <CardHeader>
                <CardTitle>Installation</CardTitle>
                <CardDescription>
                  Install the required packages for {sdk.label}.
                </CardDescription>
              </CardHeader>
              <CardContent>
                <CodeBlock code={sdk.install} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>Usage Example</CardTitle>
                <CardDescription>
                  Authenticate, list users, and create an organization.
                </CardDescription>
              </CardHeader>
              <CardContent>
                <CodeBlock code={sdk.usage} />
              </CardContent>
            </Card>
          </TabsContent>
        ))}
      </Tabs>
    </div>
  );
}
