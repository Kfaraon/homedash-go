#!/bin/sh
if command -v curl >/dev/null 2>&1; then
    curl -sf http://localhost:5000/health >/dev/null 2>&1
elif command -v wget >/dev/null 2>&1; then
    wget --spider -q http://localhost:5000/health >/dev/null 2>&1
else
    exit 1
fi