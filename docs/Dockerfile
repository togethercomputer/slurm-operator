# Dockerfile for local development of documentation

FROM python:3.13-slim-trixie

WORKDIR /docs
ADD requirements.txt /docs
RUN pip3 install --no-cache-dir -r requirements.txt
