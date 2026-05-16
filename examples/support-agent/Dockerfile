FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY agent.py .
ENV GATEWAY_URL=http://gateway:8080/mcp
EXPOSE 8085
CMD ["python", "agent.py"]
