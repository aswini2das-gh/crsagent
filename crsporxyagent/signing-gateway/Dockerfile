FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .

# Gunicorn needs a writable /tmp for worker temp files
RUN mkdir -p /tmp && chmod 1777 /tmp

EXPOSE 4318

CMD ["gunicorn", "--bind", "0.0.0.0:4318", "--workers", "2", "--timeout", "30", "app:app"]
