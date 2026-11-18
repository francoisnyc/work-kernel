# The web UI: built with Node, served by nginx as a non-root user. The
# build context is web/ (docker-compose.yml).
FROM node:24-alpine AS build
WORKDIR /web
# Dependencies first, so a code change does not install them again.
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY . .
# DEV ONLY: 1 compiles the "act as" picker in. Only the compose stack sets
# it; a build without it has no picker and no dev-user code at all.
ARG VITE_DEV_USERS=
ENV VITE_DEV_USERS=${VITE_DEV_USERS}
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29-alpine AS web
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /web/dist /usr/share/nginx/html
EXPOSE 8080
