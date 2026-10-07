# Stratus
Polyglot commerce platform on AWS EKS.

# Services
Payment service - Go Language - Runs on Port 8083
API-gateway service - NodeJS - Runs on Port 8080
Order service - Java/Spring Boot - Runs on Port 8082
Inventory service - Go Language - Runs on Port 8084 - DynamoDB
User service - .NET - Runs on Port 8081 - PostgreSQL

# Why distroless/Chiseled?
For most of the services we are using distroless/chiseled images in the final stage of the image. Because these dont have OS tools, No Shell just the runtime which inturn become very small image and very secure.
 
     Service	                 Runtime image	                        Contains
payment, inventory (Go)	     distroless/static-debian13	         CA certs, tzdata, a user. No runtime needed.
api-gateway (Node)	         distroless/nodejs24-debian13	     The Node runtime, no shell
order-service (Java)	     distroless/java21-debian13	         The Java runtime (JRE), no shell
user-service (.NET)	         dotnet/aspnet:10.0-noble-chiseled	 The .NET + ASP.NET Core runtime, no shell. Google  doesn't publish a .NET distroless, so Microsoft's "chiseled" image fills that role.
catalog, fraud (Python)	     python:3.x-slim              	     Python's distroless has no version choice, so slim is the common production pick

# If there is no Shell, How do you debug?
Not by baking a shell into the production image. In Kubernetes you attach a temporary debug container next to the running one: kubectl debug -it <pod> --image=busybox --target=<container>.