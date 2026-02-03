
import subprocess

def get_logs():
    try:
        # Run docker logs command and capture output
        result = subprocess.run(['docker', 'logs', '116be891b7fd', '--tail', '200'], 
                              capture_output=True, 
                              text=True, 
                              encoding='utf-8')
        
        # Write to file
        with open('full_logs.txt', 'w', encoding='utf-8') as f:
            f.write(result.stdout)
            f.write(result.stderr)
            
        print("Logs saved to full_logs.txt")
        
    except Exception as e:
        print(f"Error: {e}")

if __name__ == "__main__":
    get_logs()
