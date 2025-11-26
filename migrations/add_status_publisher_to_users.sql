-- Add status_publisher column to users table
ALTER TABLE users ADD COLUMN status_publisher ENUM('Active', 'Inactive', 'Suspend') DEFAULT 'Inactive';

