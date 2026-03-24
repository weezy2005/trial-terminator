# Use Microsoft's official Playwright image.
# This image comes with Chromium, Firefox, WebKit AND all their system
# dependencies pre-installed. Building this from scratch (ubuntu + apt-get
# install <50 packages>) is fragile and slow. Use the official image.
#
# ARCHITECTURAL DECISION: Why not alpine for the worker like we used for Go?
# Alpine uses musl libc. Chromium requires glibc. They're incompatible.
# The Playwright image is Ubuntu-based (jammy = Ubuntu 22.04) — it works.
FROM mcr.microsoft.com/playwright:v1.43.0-jammy

WORKDIR /app

# Install Node dependencies before copying source code — same layer caching
# strategy as the Go Dockerfile. package.json changes rarely; src/ changes often.
COPY package.json ./
RUN npm install --omit=dev

# Copy source code.
COPY src/ ./src/

CMD ["node", "src/index.js"]
