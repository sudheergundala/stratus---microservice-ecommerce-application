##### ......................Stage1: Build stage.................... ############

FROM node:24-slim AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
# Deletes node_modules entirely before installing. It never builds on top of an old install, so leftovers can't sneak in.
# Installs exactly what package-lock.json lists: exact versions, and it verifies each download's checksum (the integrity field in the lock file).
# Never changes the lock file.
# Refuses to run if package.json and the lock file disagree.
COPY tsconfig.json ./
COPY src ./src
RUN npm run build
# npm run build runs the tcs(typescript compiler) to change the Typescript to plain javascript that node can run. It looks for the build script from package.json:build:tsc.
# tsc reads tsconfig.json to learn what to compile and where to put it:
#.  "include": ["src"] means compile only files in src/.
#   "rootDir": "src" means src/ is the top of the source tree.
#.  "outDir": "dist" means write the results to dist/, mirroring the folder structure. Inside the docker it's get created under /WORKDIR/dist
#.  "sourceMap": false means don't create .map debugging files.
# It checks types first. If there's a type error, it reports it and the build fails.
# It writes one .js file per .ts file.

### .....................Stage2: Production dependencies only............................ ####

FROM node:24-slim AS prod-dep
WORKDIR /app
COPY package.json package-lock.json ./

# Production dependencies only. No typescript compiler or type definitions.
RUN npm ci --omit=dev 

### ......................Stage3: Run stage............................. ##########

# Node only. no npm, no shell required in run time. so we are using distroless. Also the enterypoint is node. So we are not mentioning node(cmd ["node","dist/server.js"]) again at cmd
FROM gcr.io/distroless/nodejs24-debian13:nonroot
# Many Node libraries switch to production behavior (less debug output, faster paths) only when this is set.
ENV NODE_ENV=production
WORKDIR /app
COPY --from=build /app/dist /app/dist
COPY --from=build /app/package.json /app/package.json
COPY --from=prod-dep /app/node_modules /app/node_modules
# Built-in "node" user, by numeric UID:GID so Kubernetes can verify non-root
USER 65532:65532
EXPOSE 8080
CMD ["dist/server.js"]
