import hashlib,json,pathlib,tempfile,unittest
from storage import cache_dataset,validate_manifest
class StorageTest(unittest.TestCase):
 def manifest(self):return {'version':1,'files':[{'path':'small/a.dat','size':3,'sha256':hashlib.sha256(b'abc').hexdigest(),'version_id':'immutable-v1'}]}
 def test_atomic_resume_and_no_partial_available(self):
  m=self.manifest();identity=hashlib.sha256(json.dumps(m,sort_keys=True,separators=(',',':')).encode()).hexdigest()
  with tempfile.TemporaryDirectory() as root:
   def bad(f,p):p.write_bytes(b'xyz')
   with self.assertRaises(RuntimeError):cache_dataset(root,identity,m,bad)
   self.assertFalse((pathlib.Path(root)/identity).exists())
   def good(f,p):p.write_bytes(b'abc')
   cache_dataset(root,identity,m,good)
   self.assertEqual((pathlib.Path(root)/identity/'small/a.dat').read_bytes(),b'abc')
 def test_traversal_version_checksum(self):
  for path in ['../a','/a','a\\b','a/../b','a//b']:
   m=self.manifest();m['files'][0]['path']=path
   with self.assertRaises(ValueError):validate_manifest(m)
  m=self.manifest();m['files'][0]['version_id']=''
  with self.assertRaises(ValueError):validate_manifest(m)
if __name__=='__main__':unittest.main()
