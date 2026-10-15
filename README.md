# Stratus
Polyglot commerce platform on AWS EKS.

# Services
Payment service - Go Language - Runs on Port 8083
API-gateway service - NodeJS - Runs on Port 8080
Order service - Java/Spring Boot - Runs on Port 8082
Inventory service - Go Language - Runs on Port 8084 - DynamoDB
User service - .NET - Runs on Port 8081 - PostgreSQL
Notification service - .NET - Runs on Port 8087
Fraud service - Python 3.13 + FastAPI + an ML model - Runs on Port 8086

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

# Docker compose VS EKS

In compose	                                                       In production (EKS)
depends_on + one-shot *-migrate services	                    Argo CD PreSync migration Jobs
read_only, cap_drop, no-new-privileges, tmpfs	               Pod securityContext
stop_grace_period: 30s	                                        terminationGracePeriodSeconds: 30
deploy.resources.limits.memory	                              resources.limits.memory
backend network with internal: true	                         Private subnets plus NetworkPolicy
Only the gateway published, on 127.0.0.1	                    Only the ALB is public
postgres, dynamodb-local, sqs-local, ses-local	               RDS, DynamoDB, SQS, SES (Terraform,