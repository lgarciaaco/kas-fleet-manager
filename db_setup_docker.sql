CREATE USER postgres with password 'postgres';
GRANT ALL PRIVILEGES ON DATABASE serviceapitests TO postgres;
GRANT ALL PRIVILEGES ON DATABASE serviceapitests TO managed_services_api;